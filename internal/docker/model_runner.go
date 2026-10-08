package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultModelRunnerBaseURL = "http://localhost:12434"
	modelRunnerBaseEnv        = "CONNECT_DMR_BASE_URL"
	modelRunnerBasesEnv       = "CONNECT_DMR_BASE_URLS"
	modelRunnerPullTimeout    = 30 * time.Minute
)

var (
	modelRunnerEnableOnce sync.Once
	modelRunnerEnableErr  error
)

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type ModelRunnerStatus struct {
	Available bool   `json:"available"`
	BaseURL   string `json:"base_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ModelRunnerConfigureRequest struct {
	Model            string   `json:"model"`
	ContextSize      *int     `json:"context_size,omitempty"`
	ResetContextSize bool     `json:"reset_context_size,omitempty"`
	RuntimeFlags     []string `json:"runtime_flags,omitempty"`
}

type ModelRunnerReinstallRunnerRequest struct {
	Backend string `json:"backend"`
	GPU     string `json:"gpu,omitempty"`
}

type modelRunnerClient struct {
	httpClient *http.Client
	baseURLs   []string
}

func newModelRunnerClient() *modelRunnerClient {
	return &modelRunnerClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURLs:   modelRunnerBaseURLsFromEnv(),
	}
}

func modelRunnerBaseURLsFromEnv() []string {
	seen := make(map[string]struct{})
	var out []string

	add := func(raw string) {
		v := strings.TrimSpace(raw)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	if v := os.Getenv(modelRunnerBasesEnv); v != "" {
		for _, part := range strings.Split(v, ",") {
			add(part)
		}
	}
	add(os.Getenv(modelRunnerBaseEnv))
	add(defaultModelRunnerBaseURL)

	return out
}

func (c *modelRunnerClient) probe(ctx context.Context) (string, error) {
	var lastErr error

	for _, baseURL := range c.baseURLs {
		_, statusCode, err := c.request(ctx, http.MethodGet, baseURL, "/engines/v1/models", nil)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", baseURL, err)
			continue
		}
		if statusCode >= 200 && statusCode < 500 {
			return baseURL, nil
		}
		lastErr = fmt.Errorf("%s: unexpected status %d", baseURL, statusCode)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no model runner base URL candidates configured")
	}

	return "", lastErr
}

func (c *modelRunnerClient) listModels(ctx context.Context, baseURL string) (json.RawMessage, error) {
	body, statusCode, err := c.request(ctx, http.MethodGet, baseURL, "/models", nil)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed {
		// Some runners expose OpenAI-compatible list endpoint only.
		body, statusCode, err = c.request(ctx, http.MethodGet, baseURL, "/engines/v1/models", nil)
		if err != nil {
			return nil, err
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, fmt.Errorf("model runner list models failed: status=%d body=%s", statusCode, strings.TrimSpace(string(body)))
	}

	return json.RawMessage(body), nil
}

func (c *modelRunnerClient) pullModel(ctx context.Context, baseURL string, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model is required")
	}

	payload := map[string]string{"from": model}
	body, statusCode, err := c.requestWithTimeout(ctx, modelRunnerPullTimeout, http.MethodPost, baseURL, "/models/create", payload)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("model runner pull model failed: status=%d body=%s", statusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (c *modelRunnerClient) deleteModel(ctx context.Context, baseURL string, model string) error {
	model = strings.Trim(model, "/")
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("model must be in namespace/name format")
	}

	deletePath := path.Join("/models", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	body, statusCode, err := c.request(ctx, http.MethodDelete, baseURL, deletePath, nil)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("model runner delete model failed: status=%d body=%s", statusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (c *modelRunnerClient) request(ctx context.Context, method, baseURL, requestPath string, payload any) ([]byte, int, error) {
	return c.requestWithTimeout(ctx, 0, method, baseURL, requestPath, payload)
}

func (c *modelRunnerClient) requestWithTimeout(ctx context.Context, timeout time.Duration, method, baseURL, requestPath string, payload any) ([]byte, int, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	reqURL := baseURL + requestPath

	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request payload: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := c.httpClient
	if timeout > 0 {
		clone := *c.httpClient
		clone.Timeout = timeout
		client = &clone
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s %s: %w", method, reqURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

func (s *Service) EnsureModelRunnerEnabled(ctx context.Context) error {
	modelRunnerEnableOnce.Do(func() {
		enableCtx := ctx
		cancel := func() {}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			enableCtx, cancel = context.WithTimeout(ctx, 20*time.Second)
		}
		defer cancel()

		runCmd := s.runCmd
		if runCmd == nil {
			runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, fmt.Errorf("command runner is not configured")
			}
		}

		// Docker Desktop bootstrap is a Windows concern in our supported targets.
		if runtime.GOOS == "windows" {
			if err := runDockerCommands(enableCtx, runCmd,
				[]string{"desktop", "enable", "model-runner", "--tcp=12434"},
				[]string{"desktop", "enable", "model-runner"},
			); err != nil {
				modelRunnerEnableErr = err
				return
			}
		}

		// On Linux hosts docker model can be unavailable; treat as a graceful no-op.
		if !isDockerModelCommandAvailable(enableCtx, runCmd) {
			modelRunnerEnableErr = nil
			return
		}

		if err := runDockerCommands(enableCtx, runCmd,
			[]string{"model", "install-runner", "--gpu", "nvidia"},
			[]string{"model", "install-runner", "--gpu", "cuda"},
			[]string{"model", "install-runner"},
		); err != nil {
			modelRunnerEnableErr = err
			return
		}

		if err := runDockerCommands(enableCtx, runCmd,
			[]string{"model", "start-runner", "--gpu", "nvidia"},
			[]string{"model", "start-runner", "--gpu", "cuda"},
			[]string{"model", "start-runner"},
		); err != nil {
			modelRunnerEnableErr = err
			return
		}

		modelRunnerEnableErr = nil
	})

	return modelRunnerEnableErr
}

func runDockerCommands(ctx context.Context, runCmd commandRunner, commands ...[]string) error {
	var hardErrs []string
	for _, args := range commands {
		out, err := runCmd(ctx, "docker", args...)
		if err == nil {
			return nil
		}
		if isBenignDMREnableError(out, err) {
			// "Already enabled/installed/running" is success for idempotent bootstrap.
			msg := strings.ToLower(strings.TrimSpace(string(out)))
			if msg == "" {
				msg = strings.ToLower(strings.TrimSpace(err.Error()))
			}
			if strings.Contains(msg, "already running") ||
				strings.Contains(msg, "already installed") ||
				strings.Contains(msg, "already enabled") {
				return nil
			}
			continue
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		hardErrs = append(hardErrs, fmt.Sprintf("docker %s: %s", strings.Join(args, " "), msg))
	}
	if len(hardErrs) == 0 {
		return nil
	}
	return errors.New(strings.Join(hardErrs, "; "))
}

func isDockerModelCommandAvailable(ctx context.Context, runCmd commandRunner) bool {
	out, err := runCmd(ctx, "docker", "model", "--help")
	if err == nil {
		return true
	}
	return !isBenignDMREnableError(out, err)
}

func (s *Service) IsModelRunnerCommandAvailable(ctx context.Context) bool {
	if s == nil || s.runCmd == nil {
		return false
	}
	return isDockerModelCommandAvailable(ctx, s.runCmd)
}

func (s *Service) ResolveModelRunnerBaseURL(ctx context.Context) (string, error) {
	client := newModelRunnerClient()
	return client.probe(ctx)
}

func isBenignDMREnableError(out []byte, err error) bool {
	if err == nil {
		return true
	}

	msg := strings.ToLower(strings.TrimSpace(string(out)))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(err.Error()))
	}

	switch {
	case strings.Contains(msg, "executable file not found"),
		strings.Contains(msg, "file not found"),
		strings.Contains(msg, "unknown command"),
		strings.Contains(msg, "is not a docker command"),
		strings.Contains(msg, "unknown flag"),
		strings.Contains(msg, "unknown shorthand flag"),
		strings.Contains(msg, "docker desktop") && strings.Contains(msg, "not available"),
		strings.Contains(msg, "docker desktop is not running"),
		strings.Contains(msg, "cannot connect to the docker daemon"),
		strings.Contains(msg, "is the docker daemon running"),
		strings.Contains(msg, "error during connect"),
		strings.Contains(msg, "context") && strings.Contains(msg, "not found"),
		strings.Contains(msg, "engine only"),
		strings.Contains(msg, "requires docker engine"),
		strings.Contains(msg, "gpu") && strings.Contains(msg, "not supported"),
		strings.Contains(msg, "cuda") && strings.Contains(msg, "not available"),
		strings.Contains(msg, "no compatible gpu"),
		strings.Contains(msg, "already running"),
		strings.Contains(msg, "already installed"),
		strings.Contains(msg, "already enabled"),
		strings.Contains(msg, "unknown settings keys: [enableinferencetcp]"),
		strings.Contains(msg, "settings format not recognized"):
		return true
	default:
		return false
	}
}

func (s *Service) ModelRunnerConfigure(ctx context.Context, req ModelRunnerConfigureRequest) (ModelRunnerStatus, string, error) {
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		return ModelRunnerStatus{}, "", fmt.Errorf("model is required")
	}
	if req.ContextSize != nil && *req.ContextSize < 0 {
		return ModelRunnerStatus{}, "", fmt.Errorf("context_size must be >= 0")
	}

	runCmd := s.runCmd
	if runCmd == nil {
		return ModelRunnerStatus{}, "", fmt.Errorf("docker command runner is not configured")
	}

	args := []string{"model", "configure"}
	if req.ResetContextSize {
		args = append(args, "--context-size", "-1")
	} else if req.ContextSize != nil {
		args = append(args, "--context-size", fmt.Sprintf("%d", *req.ContextSize))
	}
	args = append(args, req.Model)
	if len(req.RuntimeFlags) > 0 {
		args = append(args, "--")
		args = append(args, req.RuntimeFlags...)
	}

	out, err := runCmd(ctx, "docker", args...)
	st := s.ModelRunnerStatus(ctx)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return st, msg, fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), msg)
	}

	return st, strings.TrimSpace(string(out)), nil
}

func (s *Service) ModelRunnerReinstallRunner(ctx context.Context, req ModelRunnerReinstallRunnerRequest) (ModelRunnerStatus, string, error) {
	backend := strings.TrimSpace(strings.ToLower(req.Backend))
	switch backend {
	case "llama.cpp", "vllm", "diffusers":
	default:
		return ModelRunnerStatus{}, "", fmt.Errorf("backend must be one of: llama.cpp, vllm, diffusers")
	}

	gpu := strings.TrimSpace(strings.ToLower(req.GPU))
	if gpu == "" {
		gpu = "auto"
	}
	switch gpu {
	case "none", "auto", "cuda", "rocm", "musa", "cann":
	default:
		return ModelRunnerStatus{}, "", fmt.Errorf("gpu must be one of: none, auto, cuda, rocm, musa, cann")
	}

	runCmd := s.runCmd
	if runCmd == nil {
		return ModelRunnerStatus{}, "", fmt.Errorf("docker command runner is not configured")
	}

	args := []string{"model", "reinstall-runner", "--backend", backend, "--gpu", gpu}
	out, err := runCmd(ctx, "docker", args...)
	st := s.ModelRunnerStatus(ctx)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return st, msg, fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), msg)
	}

	return st, strings.TrimSpace(string(out)), nil
}

func (s *Service) ModelRunnerInspect(ctx context.Context, model string, openAI bool) (json.RawMessage, ModelRunnerStatus, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, ModelRunnerStatus{}, fmt.Errorf("model is required")
	}

	runCmd := s.runCmd
	if runCmd == nil {
		return nil, ModelRunnerStatus{}, fmt.Errorf("docker command runner is not configured")
	}

	args := []string{"model", "inspect", model}
	if openAI {
		args = append(args, "--openai")
	}

	out, err := runCmd(ctx, "docker", args...)
	st := s.ModelRunnerStatus(ctx)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, st, fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), msg)
	}

	payload := bytes.TrimSpace(out)
	if len(payload) == 0 {
		payload = []byte("null")
	}
	if !json.Valid(payload) {
		return nil, st, fmt.Errorf("docker model inspect returned non-json output")
	}

	return json.RawMessage(payload), st, nil
}

func (s *Service) ModelRunnerStatus(ctx context.Context) ModelRunnerStatus {
	client := newModelRunnerClient()
	baseURL, err := client.probe(ctx)
	if err != nil {
		return ModelRunnerStatus{
			Available: false,
			Error:     err.Error(),
		}
	}

	return ModelRunnerStatus{
		Available: true,
		BaseURL:   baseURL,
	}
}

func (s *Service) ModelRunnerListModels(ctx context.Context) (json.RawMessage, ModelRunnerStatus, error) {
	client := newModelRunnerClient()
	baseURL, err := client.probe(ctx)
	if err != nil {
		st := ModelRunnerStatus{Available: false, Error: err.Error()}
		return nil, st, err
	}

	models, err := client.listModels(ctx, baseURL)
	if err != nil {
		st := ModelRunnerStatus{Available: true, BaseURL: baseURL, Error: err.Error()}
		return nil, st, err
	}

	return models, ModelRunnerStatus{Available: true, BaseURL: baseURL}, nil
}

func (s *Service) ModelRunnerPullModel(ctx context.Context, model string) (ModelRunnerStatus, error) {
	client := newModelRunnerClient()
	baseURL, err := client.probe(ctx)
	if err != nil {
		st := ModelRunnerStatus{Available: false, Error: err.Error()}
		return st, err
	}

	if err := client.pullModel(ctx, baseURL, model); err != nil {
		st := ModelRunnerStatus{Available: true, BaseURL: baseURL, Error: err.Error()}
		return st, err
	}

	return ModelRunnerStatus{Available: true, BaseURL: baseURL}, nil
}

func (s *Service) ModelRunnerDeleteModel(ctx context.Context, model string) (ModelRunnerStatus, error) {
	client := newModelRunnerClient()
	baseURL, err := client.probe(ctx)
	if err != nil {
		st := ModelRunnerStatus{Available: false, Error: err.Error()}
		return st, err
	}

	if err := client.deleteModel(ctx, baseURL, model); err != nil {
		st := ModelRunnerStatus{Available: true, BaseURL: baseURL, Error: err.Error()}
		return st, err
	}

	return ModelRunnerStatus{Available: true, BaseURL: baseURL}, nil
}
