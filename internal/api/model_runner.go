package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/swaggest/usecase/status"
)

type ConfigureStatusOutput struct {
	Available bool   `json:"available"`
	BaseURL   string `json:"base_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

func publicModelRunnerStatusError(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return "Model runner is unavailable. Check the connector logs for details."
}

func (s APIService) ConfigureStatus(ctx context.Context, _ struct{}, output *ConfigureStatusOutput) error {
	st := s.dockerService().ModelRunnerStatus(ctx)
	output.Available = st.Available
	output.BaseURL = st.BaseURL
	output.Error = publicModelRunnerStatusError(st.Error)
	return nil
}

type ConfigurePullModelInput struct {
	Model string `json:"model"`
}

type ConfigurePullModelOutput struct {
	Success bool                  `json:"success"`
	Status  ConfigureStatusOutput `json:"status"`
}

func (s APIService) ConfigurePullModel(ctx context.Context, input ConfigurePullModelInput, output *ConfigurePullModelOutput) error {
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return status.Wrap(fmt.Errorf("model is required"), status.InvalidArgument)
	}

	st, err := s.dockerService().ModelRunnerPullModel(ctx, input.Model)
	output.Status = ConfigureStatusOutput{
		Available: st.Available,
		BaseURL:   st.BaseURL,
		Error:     publicModelRunnerStatusError(st.Error),
	}
	if err != nil {
		return status.Wrap(fmt.Errorf("pull model: %w", err), status.FailedPrecondition)
	}

	output.Success = true
	return nil
}

type ConfigureDeleteModelInput struct {
	Model string `query:"model"`
}

type ConfigureDeleteModelOutput struct {
	Success bool                  `json:"success"`
	Status  ConfigureStatusOutput `json:"status"`
}

func (s APIService) ConfigureDeleteModel(ctx context.Context, input ConfigureDeleteModelInput, output *ConfigureDeleteModelOutput) error {
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return status.Wrap(fmt.Errorf("model is required"), status.InvalidArgument)
	}

	st, err := s.dockerService().ModelRunnerDeleteModel(ctx, input.Model)
	output.Status = ConfigureStatusOutput{
		Available: st.Available,
		BaseURL:   st.BaseURL,
		Error:     publicModelRunnerStatusError(st.Error),
	}
	if err != nil {
		return status.Wrap(fmt.Errorf("delete model: %w", err), status.FailedPrecondition)
	}

	output.Success = true
	return nil
}

type ConfigureModelInput struct {
	Model            string   `json:"model"`
	ContextSize      *int     `json:"context_size,omitempty"`
	ResetContextSize bool     `json:"reset_context_size,omitempty"`
	RuntimeFlags     []string `json:"runtime_flags,omitempty"`
}

type ConfigureModelOutput struct {
	Success bool                  `json:"success"`
	Status  ConfigureStatusOutput `json:"status"`
	Output  string                `json:"output,omitempty"`
}

func (s APIService) ConfigureModel(ctx context.Context, input ConfigureModelInput, output *ConfigureModelOutput) error {
	st, cmdOut, err := s.dockerService().ModelRunnerConfigure(ctx, internaldocker.ModelRunnerConfigureRequest{
		Model:            input.Model,
		ContextSize:      input.ContextSize,
		ResetContextSize: input.ResetContextSize,
		RuntimeFlags:     input.RuntimeFlags,
	})
	output.Status = ConfigureStatusOutput{
		Available: st.Available,
		BaseURL:   st.BaseURL,
		Error:     publicModelRunnerStatusError(st.Error),
	}
	output.Output = cmdOut
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "required") || strings.Contains(strings.ToLower(err.Error()), "must be") {
			return status.Wrap(fmt.Errorf("configure model: %w", err), status.InvalidArgument)
		}
		return status.Wrap(fmt.Errorf("configure model: %w", err), status.FailedPrecondition)
	}

	output.Success = true
	return nil
}

type ConfigureRunnerReinstallInput struct {
	Backend string `json:"backend"`
	GPU     string `json:"gpu,omitempty"`
}

type ConfigureRunnerReinstallOutput struct {
	Success bool                  `json:"success"`
	Status  ConfigureStatusOutput `json:"status"`
	Output  string                `json:"output,omitempty"`
}

func (s APIService) ConfigureRunnerReinstall(ctx context.Context, input ConfigureRunnerReinstallInput, output *ConfigureRunnerReinstallOutput) error {
	st, cmdOut, err := s.dockerService().ModelRunnerReinstallRunner(ctx, internaldocker.ModelRunnerReinstallRunnerRequest{
		Backend: input.Backend,
		GPU:     input.GPU,
	})
	output.Status = ConfigureStatusOutput{
		Available: st.Available,
		BaseURL:   st.BaseURL,
		Error:     publicModelRunnerStatusError(st.Error),
	}
	output.Output = cmdOut
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "must be") || strings.Contains(strings.ToLower(err.Error()), "required") {
			return status.Wrap(fmt.Errorf("reinstall runner: %w", err), status.InvalidArgument)
		}
		return status.Wrap(fmt.Errorf("reinstall runner: %w", err), status.FailedPrecondition)
	}

	output.Success = true
	return nil
}

type ConfigureInspectModelInput struct {
	Model  string `query:"model"`
	OpenAI bool   `query:"openai"`
}

type ConfigureInspectModelOutput struct {
	Status ConfigureStatusOutput `json:"status"`
	Model  json.RawMessage       `json:"model,omitempty"`
}

func (s APIService) ConfigureInspectModel(ctx context.Context, input ConfigureInspectModelInput, output *ConfigureInspectModelOutput) error {
	model, st, err := s.dockerService().ModelRunnerInspect(ctx, input.Model, input.OpenAI)
	output.Status = ConfigureStatusOutput{
		Available: st.Available,
		BaseURL:   st.BaseURL,
		Error:     publicModelRunnerStatusError(st.Error),
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "required") {
			return status.Wrap(fmt.Errorf("inspect model config: %w", err), status.InvalidArgument)
		}
		return status.Wrap(fmt.Errorf("inspect model config: %w", err), status.FailedPrecondition)
	}

	output.Model = model
	return nil
}

func (s APIService) EnginesProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.dockerService().EnsureModelRunnerEnabled(r.Context()); err != nil {
		// Best effort, continue to probe/proxy.
	}

	baseURL, err := s.dockerService().ResolveModelRunnerBaseURL(r.Context())
	if err != nil {
		log.Printf("request_id=%s model_runner_unavailable=%v", middleware.GetReqID(r.Context()), err)
		http.Error(w, "Model runner is unavailable. Check the connector logs for details.", http.StatusPreconditionFailed)
		return
	}

	target, err := url.Parse(baseURL)
	if err != nil {
		log.Printf("request_id=%s model_runner_url_error=%v", middleware.GetReqID(r.Context()), err)
		http.Error(w, "Model runner is temporarily unavailable. Please try again.", http.StatusInternalServerError)
		return
	}

	originalPath := r.URL.Path
	if rp := chi.RouteContext(r.Context()); rp != nil {
		if star := rp.URLParam("*"); star != "" {
			originalPath = "/engines/" + strings.TrimPrefix(star, "/")
		}
	}
	if !strings.HasPrefix(originalPath, "/engines/") {
		originalPath = "/engines/v1/models"
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.URL.Path = originalPath
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		log.Printf("request_id=%s model_runner_proxy_error=%v", middleware.GetReqID(req.Context()), proxyErr)
		http.Error(rw, "Model runner is temporarily unavailable. Please try again.", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
