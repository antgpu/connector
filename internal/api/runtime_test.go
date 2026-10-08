package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/go-chi/chi/v5"
)

func TestRuntimeEndpointAllowlist(t *testing.T) {
	for _, tc := range []struct {
		kind, method, path string
		allowed            bool
	}{
		{"unsloth", "POST", "/api/train/start", true},
		{"unsloth", "GET", "/api/train/progress", true},
		{"unsloth", "POST", "/api/train/start-requests/request-1/acknowledge", true},
		{"unsloth", "POST", "/api/hub/datasets/upload", true},
		{"unsloth", "GET", "/api/auth/status", false},
		{"unsloth", "POST", "/api/auth/login", false},
		{"unsloth", "GET", "/api/train/runs/../../auth", false},
		{"unsloth", "GET", "/api/train/runs/%2e%2e", false},
		{"unsloth", "GET", "/api/train/runs\\other", false},
		{"unsloth", "DELETE", "/api/train/start", false},
		{"unsloth", "GET", "/api/train/runs/1/files/secret", false},
		{"openai", "POST", "/v1/chat/completions", true},
		{"openai", "POST", "/api/train/start", false},
		{"dmr", "POST", "/models/create", true},
		{"dmr", "DELETE", "/models/ai/smollm2", true},
		{"dmr", "POST", "/configure/reinstall", false},
		{"dmr", "GET", "/engines/v1/models", true},
		{"other", "GET", "/v1/models", false},
	} {
		if got := runtimeEndpointAllowed(tc.kind, tc.method, tc.path); got != tc.allowed {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestRuntimeInspectionRedactsServiceCredentials(t *testing.T) {
	env := []string{"PATH=/bin", "ANTGPU_RUNTIME_TOKEN=private", "ANTGPU_UNSLOTH_USERNAME=service", "ANTGPU_UNSLOTH_PASSWORD=password", "UNSLOTH_STUDIO_PASSWORD=private-bootstrap"}
	redacted := redactRuntimeEnvironment(env)
	if redacted[4] != "UNSLOTH_STUDIO_PASSWORD=[redacted]" {
		t.Fatal("bootstrap credential leaked")
	}
	if redacted[0] != env[0] || redacted[1] != "ANTGPU_RUNTIME_TOKEN=[redacted]" || redacted[2] != "ANTGPU_UNSLOTH_USERNAME=[redacted]" || redacted[3] != "ANTGPU_UNSLOTH_PASSWORD=[redacted]" {
		t.Fatal("runtime credentials leaked in container inspection")
	}
	if env[1] != "ANTGPU_RUNTIME_TOKEN=private" {
		t.Fatal("internal environment mutated")
	}
}

func runtimeTestRouter(target internaldocker.RuntimeTarget) http.Handler {
	r := chi.NewRouter()
	// Production runtime routes are raw streaming handlers with explicit auth;
	// swaggest gzip buffers these responses and must not wrap this path.
	r.HandleFunc("/runtime/*", func(w http.ResponseWriter, r *http.Request) { serveRuntime(w, r, target) })
	return r
}

func TestRuntimeProxyStreamsWithoutLeakingPlatformCredentials(t *testing.T) {
	disconnected := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "trace=1" {
			t.Errorf("unexpected URL %s", r.URL)
		}
		for _, name := range []string{"Cookie", "X-Machine-Token", "X-User-ID", "X-Forwarded-For", "Origin"} {
			if r.Header.Get(name) != "" {
				t.Errorf("leaked %s", name)
			}
		}
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Error("runtime token missing")
		}
		if r.Header.Get("Last-Event-ID") != "17" {
			t.Error("resume cursor missing")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"stream":true}` {
			t.Errorf("body changed: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "runtime=secret")
		w.Header().Set("Location", "http://private-container")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	proxy := httptest.NewServer(runtimeTestRouter(internaldocker.RuntimeTarget{Kind: "openai", URL: u, Token: "runtime-secret"}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", proxy.URL+"/runtime/api/v1/chat/completions?trace=1", strings.NewReader(`{"stream":true}`))
	for _, name := range []string{"Cookie", "X-Machine-Token", "X-User-ID", "X-Forwarded-For", "Origin", "Authorization"} {
		req.Header.Set(name, "platform-secret")
	}
	req.Header.Set("Last-Event-ID", "17")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("stream did not flush: %q %v", line, err)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Location") != "" {
		t.Fatal("runtime response credentials/URL leaked")
	}
	cancel()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("upstream did not cancel")
	}
}

func TestRuntimeLoginAndPasswordChangeGate(t *testing.T) {
	for _, mustChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "provisioned", true: "default-password"}[mustChange], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/auth/login" || r.Method != "POST" {
					t.Error("wrong login contract")
				}
				if mustChange {
					_, _ = io.WriteString(w, `{"access_token":"secret","must_change_password":true}`)
				} else {
					_, _ = io.WriteString(w, `{"access_token":"secret","must_change_password":false}`)
				}
			}))
			defer upstream.Close()
			u, _ := url.Parse(upstream.URL)
			token, err := runtimeLogin(context.Background(), internaldocker.RuntimeTarget{URL: u, Username: "service", Password: "private"})
			if mustChange && err == nil {
				t.Fatal("default password accepted")
			}
			if !mustChange && (err != nil || token != "secret") {
				t.Fatalf("login failed: %v", err)
			}
		})
	}
}

func TestRuntimeCapabilitiesNeverExposeConnectionDetails(t *testing.T) {
	u, _ := url.Parse("http://172.18.0.2:8000")
	rr := httptest.NewRecorder()
	runtimeTestRouter(internaldocker.RuntimeTarget{Kind: "unsloth", URL: u, Token: "secret", Password: "password"}).ServeHTTP(rr, httptest.NewRequest("GET", "/runtime/capabilities", nil))
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	for _, private := range []string{"172.18.0.2", "secret", "password", "8000"} {
		if strings.Contains(rr.Body.String(), private) {
			t.Errorf("capabilities leaked %s", private)
		}
	}
}
