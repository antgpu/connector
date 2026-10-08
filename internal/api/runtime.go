package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/go-chi/chi/v5"
)

// These transports intentionally have no environment proxy: container credentials
// and inference payloads must stay on the connector's private Docker network.
var runtimeTransport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 10 * time.Minute, IdleConnTimeout: 90 * time.Second}

type runtimeSession struct {
	token   string
	expires time.Time
}

var runtimeSessions = struct {
	sync.Mutex
	entries map[[32]byte]runtimeSession
}{entries: make(map[[32]byte]runtimeSession)}

func runtimeSessionKey(target internaldocker.RuntimeTarget) [32]byte {
	return sha256.Sum256([]byte(target.URL.String() + "\x00" + target.Username + "\x00" + target.Password))
}

func redactRuntimeEnvironment(env []string) []string {
	result := append([]string(nil), env...)
	for i, entry := range result {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ANTGPU_RUNTIME_TOKEN" || key == "ANTGPU_UNSLOTH_USERNAME" || key == "ANTGPU_UNSLOTH_PASSWORD" || key == "UNSLOTH_STUDIO_PASSWORD" {
			result[i] = key + "=[redacted]"
		}
	}
	return result
}

func runtimeEndpointAllowed(kind, method, p string) bool {
	if path.Clean(p) != p || strings.ContainsAny(p, "%\\?#\x00\r\n") {
		return false
	}
	read := method == http.MethodGet
	write := method == http.MethodPost
	if kind == "dmr" {
		return (read && p == "/models") || (write && p == "/models/create") ||
			(method == http.MethodDelete && strings.HasPrefix(p, "/models/") && len(strings.TrimPrefix(p, "/models/")) > 0) ||
			(read && p == "/engines/v1/models") ||
			(write && (p == "/engines/v1/chat/completions" || p == "/engines/v1/completions" || p == "/engines/v1/embeddings"))
	}
	if kind == "openai" {
		return (read && p == "/v1/models") || (write && (p == "/v1/chat/completions" || p == "/v1/completions" || p == "/v1/embeddings"))
	}
	if kind != "unsloth" {
		return false
	}
	if read {
		switch p {
		case "/api/system/hardware", "/api/models/local", "/api/train/status", "/api/train/metrics", "/api/train/progress", "/api/train/runs", "/api/inference/status", "/api/inference/models", "/api/inference/load-progress", "/api/export/status", "/api/export/logs", "/api/export/logs/stream":
			return true
		}
		for _, prefix := range []string{"/api/models/config/", "/api/models/check-vision/", "/api/models/check-embedding/"} {
			if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
				return true
			}
		}
	}
	if write {
		switch p {
		case "/api/train/start", "/api/train/stop", "/api/train/reset", "/api/hub/datasets/upload", "/api/hub/datasets/check-format", "/api/inference/load", "/api/inference/unload", "/api/inference/chat/completions", "/api/inference/completions", "/api/inference/embeddings", "/api/export/load-checkpoint", "/api/export/cleanup", "/api/export/cancel", "/api/export/export/merged", "/api/export/export/base", "/api/export/export/gguf", "/api/export/export/lora":
			return true
		}
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "train" {
		return (parts[2] == "runs" && (read || method == http.MethodPatch || method == http.MethodDelete)) || (parts[2] == "start-requests" && read)
	}
	return len(parts) == 5 && parts[0] == "api" && parts[1] == "train" && parts[2] == "start-requests" && write && (parts[4] == "acknowledge" || parts[4] == "cancel")
}

func (s APIService) ContainerRuntimeProxy(w http.ResponseWriter, r *http.Request) {
	target, err := s.dockerService().ContainerRuntime(r.Context(), chi.URLParam(r, "container_id"))
	if err != nil {
		http.Error(w, "Runtime unavailable: configure the template runtime labels and start its container.", http.StatusConflict)
		return
	}
	serveRuntime(w, r, target)
}

func (s APIService) DMRRuntimeProxy(w http.ResponseWriter, r *http.Request) {
	base, err := s.dockerService().ResolveModelRunnerBaseURL(r.Context())
	if err != nil {
		http.Error(w, "Docker Model Runner is unavailable", http.StatusServiceUnavailable)
		return
	}
	u, err := url.Parse(base)
	if err != nil {
		http.Error(w, "Invalid model runner configuration", http.StatusServiceUnavailable)
		return
	}
	serveRuntime(w, r, internaldocker.RuntimeTarget{Kind: "dmr", URL: u})
}

func serveRuntime(w http.ResponseWriter, r *http.Request, target internaldocker.RuntimeTarget) {
	suffix := chi.URLParam(r, "*")
	if suffix == "capabilities" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		capabilities := map[string]any{"kind": target.Kind, "contract_version": 1, "training": target.Kind == "unsloth", "inference": true, "model_management": target.Kind == "dmr", "dataset_upload": target.Kind == "unsloth"}
		if n := runtimeContextTokens(r.Context(), target); n > 0 {
			capabilities["context_tokens"] = n
		}
		_ = json.NewEncoder(w).Encode(capabilities)
		return
	}
	p := "/" + strings.TrimPrefix(suffix, "api/")
	if !strings.HasPrefix(suffix, "api/") || !runtimeEndpointAllowed(target.Kind, r.Method, p) {
		http.Error(w, "Unsupported runtime operation", http.StatusNotFound)
		return
	}
	if target.Kind == "unsloth" && target.Token == "" {
		var err error
		target.Token, err = runtimeLogin(r.Context(), target)
		if err != nil {
			http.Error(w, "Unsloth service authentication is unavailable; configure a provisioned runtime account.", http.StatusBadGateway)
			return
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(target.URL)
	proxy.Director = nil
	proxy.Transport = runtimeTransport
	proxy.FlushInterval = -1
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target.URL)
		pr.Out.URL.Path = p
		pr.Out.URL.RawPath = ""
		pr.Out.Host = target.URL.Host
		// Explicit end-to-end header allowlist, never forward platform identity.
		pr.Out.Header = make(http.Header)
		for _, name := range []string{"Content-Type", "Accept", "Last-Event-ID", "Range", "If-Range"} {
			if value := pr.In.Header.Get(name); value != "" {
				pr.Out.Header.Set(name, value)
			}
		}
		if target.Token != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+target.Token)
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusUnauthorized {
			runtimeSessions.Lock()
			delete(runtimeSessions.entries, runtimeSessionKey(target))
			runtimeSessions.Unlock()
		}
		resp.Header.Del("Set-Cookie")
		resp.Header.Del("Location")
		resp.Header.Del("Access-Control-Allow-Origin")
		resp.Header.Set("Cache-Control", "no-store")
		resp.Header.Set("X-Accel-Buffering", "no")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "Runtime connection interrupted", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// llama.cpp exposes its effective per-slot context in /props. Other OpenAI
// servers need not implement it: unknown is omitted, never inferred from the
// model's training context or the requested maximum output tokens.
func runtimeContextTokens(ctx context.Context, target internaldocker.RuntimeTarget) int {
	if target.Kind != "openai" || target.URL == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	u := *target.URL
	u.Path, u.RawQuery = "/props", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0
	}
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}
	client := &http.Client{Transport: runtimeTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var props struct {
		Settings struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&props) != nil {
		return 0
	}
	if props.Settings.Context <= 0 || props.Settings.Context > 10000000 {
		return 0
	}
	return props.Settings.Context
}

func runtimeLogin(ctx context.Context, target internaldocker.RuntimeTarget) (string, error) {
	if target.Username == "" || target.Password == "" {
		return "", fmt.Errorf("missing runtime credentials")
	}
	key := runtimeSessionKey(target)
	runtimeSessions.Lock()
	session, found := runtimeSessions.entries[key]
	runtimeSessions.Unlock()
	if found && time.Now().Before(session.expires) {
		return session.token, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"username": target.Username, "password": target.Password})
	u := *target.URL
	u.Path = "/api/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: runtimeTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Token      string `json:"access_token"`
		MustChange bool   `json:"must_change_password"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil || result.Token == "" || result.MustChange {
		return "", fmt.Errorf("runtime account login failed")
	}
	runtimeSessions.Lock()
	for key, session := range runtimeSessions.entries {
		if time.Now().After(session.expires) {
			delete(runtimeSessions.entries, key)
		}
	}
	if len(runtimeSessions.entries) < 128 {
		runtimeSessions.entries[key] = runtimeSession{token: result.Token, expires: time.Now().Add(30 * time.Second)}
	}
	runtimeSessions.Unlock()
	return result.Token, nil
}
