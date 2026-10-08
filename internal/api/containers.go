package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	backendDocker "github.com/antgpu/connector-protocol"
	internaldocker "github.com/antgpu/connector/internal/docker"
	pkgdocker "github.com/antgpu/connector/pkg/docker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/go-chi/chi/v5"
	"github.com/swaggest/usecase/status"
	"golang.org/x/net/websocket"
)

func (s APIService) dockerService() *internaldocker.Service {
	return s.DockerService
}

type ContainerCreateFromTemplateInput struct {
	TemplateID    int                     `json:"template_id"`
	RentID        string                  `json:"rent_id,omitempty"`
	Template      *backendDocker.Template `json:"template,omitempty"`
	CurrentUserID *int                    `header:"X-User-ID"`
}

type ContainerCreateFromTemplateOutput struct {
	ContainerID string                               `json:"container_id"`
	AutoStopped *internaldocker.RunningContainerInfo `json:"auto_stopped,omitempty"`
}

func (s APIService) ContainerCreateFromTemplate(ctx context.Context, input ContainerCreateFromTemplateInput, output *ContainerCreateFromTemplateOutput) error {
	if input.TemplateID <= 0 {
		return status.Wrap(fmt.Errorf("template_id is required"), status.InvalidArgument)
	}

	template, err := s.resolveContainerTemplate(ctx, input.TemplateID, input.Template, input.CurrentUserID)
	if err != nil {
		return err
	}
	result, err := s.dockerService().CreateFromTemplateWithOptions(ctx, *template, nil, pkgdocker.LaunchOptions{RentID: strings.TrimSpace(input.RentID)})
	if err != nil {
		return wrapDockerErr("create container from template", err)
	}

	output.ContainerID = result.ContainerID
	output.AutoStopped = result.AutoStopped
	s.refreshSpecsAsync("container_create")

	return nil
}

type ContainerIDInput struct {
	ContainerID string `path:"container_id"`
}

type ContainerStartOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerStart(ctx context.Context, input ContainerIDInput, output *ContainerStartOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	if err := s.dockerService().StartContainer(ctx, input.ContainerID); err != nil {
		return wrapDockerErr("start container", err)
	}

	output.Success = true
	return nil
}

type ContainerStopInput struct {
	ContainerID string `path:"container_id"`
	TimeoutSec  int    `query:"timeout_sec"`
}

type ContainerStopOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerStop(ctx context.Context, input ContainerStopInput, output *ContainerStopOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}
	if input.TimeoutSec < 0 {
		return status.Wrap(fmt.Errorf("timeout_sec must be >= 0"), status.InvalidArgument)
	}

	var timeout *time.Duration
	if input.TimeoutSec > 0 {
		t := time.Duration(input.TimeoutSec) * time.Second
		timeout = &t
	}

	if err := s.dockerService().StopContainer(ctx, input.ContainerID, timeout); err != nil {
		return wrapDockerErr("stop container", err)
	}

	output.Success = true
	return nil
}

type ContainerRestartInput struct {
	ContainerID string `path:"container_id"`
	TimeoutSec  int    `query:"timeout_sec"`
}

type ContainerRestartOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerRestart(ctx context.Context, input ContainerRestartInput, output *ContainerRestartOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}
	if input.TimeoutSec < 0 {
		return status.Wrap(fmt.Errorf("timeout_sec must be >= 0"), status.InvalidArgument)
	}

	var timeout *time.Duration
	if input.TimeoutSec > 0 {
		t := time.Duration(input.TimeoutSec) * time.Second
		timeout = &t
	}

	if err := s.dockerService().RestartContainer(ctx, input.ContainerID, timeout); err != nil {
		return wrapDockerErr("restart container", err)
	}

	output.Success = true
	return nil
}

type ContainerPauseOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerPause(ctx context.Context, input ContainerIDInput, output *ContainerPauseOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	if err := s.dockerService().PauseContainer(ctx, input.ContainerID); err != nil {
		return wrapDockerErr("pause container", err)
	}

	output.Success = true
	return nil
}

type ContainerUnpauseOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerUnpause(ctx context.Context, input ContainerIDInput, output *ContainerUnpauseOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	if err := s.dockerService().UnpauseContainer(ctx, input.ContainerID); err != nil {
		return wrapDockerErr("unpause container", err)
	}

	output.Success = true
	return nil
}

type ContainerStatsInput struct {
	ContainerID string `path:"container_id"`
}

type ContainerStatsOutput struct {
	Stats internaldocker.ContainerStatsSnapshot `json:"stats"`
}

type ContainerStorageOutput struct {
	Storage internaldocker.ContainerStorageSnapshot `json:"storage"`
}

func (s APIService) ContainerStorage(ctx context.Context, input ContainerIDInput, output *ContainerStorageOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}
	storage, err := s.dockerService().ContainerStorage(ctx, input.ContainerID)
	if err != nil {
		return wrapDockerErr("container storage", err)
	}
	output.Storage = storage
	return nil
}

func (s APIService) ContainerStats(ctx context.Context, input ContainerStatsInput, output *ContainerStatsOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	statsSnap, err := s.dockerService().StatsSnapshot(ctx, input.ContainerID)
	if err != nil {
		return wrapDockerErr("container stats", err)
	}

	output.Stats = statsSnap
	return nil
}

type ContainerListInput struct {
	All bool `query:"all"`
}

type ManagedContainer struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	State   string            `json:"state"`
	Status  string            `json:"status"`
	Created int64             `json:"created"`
	Ports   []container.Port  `json:"ports"`
	Labels  map[string]string `json:"labels"`
}

type ContainerListOutput struct {
	Containers []ManagedContainer `json:"containers"`
}

func (s APIService) ContainerList(ctx context.Context, input ContainerListInput, output *ContainerListOutput) error {
	items, err := s.dockerService().ListManagedContainers(ctx, input.All, nil)
	if err != nil {
		return wrapDockerErr("list containers", err)
	}

	output.Containers = make([]ManagedContainer, 0, len(items))
	for _, c := range items {
		output.Containers = append(output.Containers, ManagedContainer{
			ID:      c.ID,
			Name:    normalizeContainerName(c.Names),
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Created: c.Created,
			Ports:   c.Ports,
			Labels:  c.Labels,
		})
	}

	return nil
}

type ContainerInspectOutput struct {
	Inspect container.InspectResponse `json:"inspect"`
}

func (s APIService) ContainerInspect(ctx context.Context, input ContainerIDInput, output *ContainerInspectOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	insp, err := s.dockerService().InspectContainer(ctx, input.ContainerID)
	if err != nil {
		return wrapDockerErr("inspect container", err)
	}

	if insp.Config != nil {
		config := *insp.Config
		config.Env = redactRuntimeEnvironment(config.Env)
		insp.Config = &config
	}
	output.Inspect = insp
	return nil
}

type ContainerRemoveInput struct {
	ContainerID   string `path:"container_id"`
	Force         bool   `query:"force"`
	RemoveVolumes bool   `query:"remove_volumes"`
}

type ContainerRemoveOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerRemove(ctx context.Context, input ContainerRemoveInput, output *ContainerRemoveOutput) error {
	if input.ContainerID == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	if err := s.dockerService().RemoveContainer(ctx, input.ContainerID, input.Force, input.RemoveVolumes); err != nil {
		return wrapDockerErr("remove container", err)
	}

	output.Success = true
	s.refreshSpecsAsync("container_remove")
	return nil
}

func (s APIService) ContainerLogsWS(w http.ResponseWriter, r *http.Request) {
	containerID := chi.URLParam(r, "container_id")
	if containerID == "" {
		http.Error(w, "container_id is required", http.StatusBadRequest)
		return
	}

	follow := true
	if v := r.URL.Query().Get("follow"); v == "false" || v == "0" {
		follow = false
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}

	websocket.Server{
		Handshake: rejectBrowserWebSocketOrigin,
		Handler: websocket.Handler(func(ws *websocket.Conn) {
			defer ws.Close()

			logsReader, err := s.dockerService().Logs(r.Context(), containerID, follow, tail)
			if err != nil {
				_ = websocket.Message.Send(ws, "error: "+err.Error())
				return
			}
			defer logsReader.Close()

			raw := r.URL.Query().Get("raw")
			if raw == "1" || strings.EqualFold(raw, "true") {
				buf := make([]byte, 8*1024)
				for {
					n, readErr := logsReader.Read(buf)
					if n > 0 {
						if _, wErr := ws.Write(buf[:n]); wErr != nil {
							return
						}
					}
					if readErr != nil {
						if readErr == io.EOF {
							return
						}
						_ = websocket.Message.Send(ws, "error: "+readErr.Error())
						return
					}
				}
			}

			_, _ = stdcopy.StdCopy(&wsPrefixWriter{ws: ws, prefix: "[stdout] "}, &wsPrefixWriter{ws: ws, prefix: "[stderr] "}, logsReader)
		}),
	}.ServeHTTP(w, r)
}

func (s APIService) ContainerShellWS(w http.ResponseWriter, r *http.Request) {
	containerID := chi.URLParam(r, "container_id")
	if containerID == "" {
		http.Error(w, "container_id is required", http.StatusBadRequest)
		return
	}

	tty := true
	if v := r.URL.Query().Get("tty"); v == "false" || v == "0" {
		tty = false
	}

	websocket.Server{
		Handshake: rejectBrowserWebSocketOrigin,
		Handler: websocket.Handler(func(ws *websocket.Conn) {
			defer ws.Close()

			session, err := s.dockerService().OpenShellSession(r.Context(), containerID, tty)
			if err != nil {
				_ = websocket.Message.Send(ws, "error: "+err.Error())
				return
			}
			defer session.Close()

			done := make(chan struct{}, 2)

			go func() {
				defer func() { done <- struct{}{} }()
				_, _ = io.Copy(session.Hijacked.Conn, ws)
				_ = session.Hijacked.CloseWrite()
			}()

			go func() {
				defer func() { done <- struct{}{} }()
				_, _ = io.Copy(ws, session.Hijacked.Reader)
			}()

			<-done
		}),
	}.ServeHTTP(w, r)
}

func rejectBrowserWebSocketOrigin(_ *websocket.Config, r *http.Request) error {
	if strings.TrimSpace(r.Header.Get("Origin")) != "" {
		return fmt.Errorf("browser-origin websocket access is disabled")
	}
	return nil
}

func wrapDockerErr(op string, err error) error {
	switch {
	case errdefs.IsNotFound(err):
		return status.Wrap(fmt.Errorf("%s: %w", op, err), status.NotFound)
	case errdefs.IsInvalidParameter(err):
		return status.Wrap(fmt.Errorf("%s: %w", op, err), status.InvalidArgument)
	case errdefs.IsConflict(err):
		return status.Wrap(fmt.Errorf("%s: %w", op, err), status.Aborted)
	default:
		return status.Wrap(fmt.Errorf("%s: %w", op, err), status.Internal)
	}
}

type wsPrefixWriter struct {
	ws     *websocket.Conn
	prefix string
}

func (w *wsPrefixWriter) Write(p []byte) (int, error) {
	data := append([]byte(w.prefix), p...)
	if _, err := w.ws.Write(data); err != nil {
		return 0, err
	}
	return len(p), nil
}

func normalizeContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (s APIService) resolveContainerTemplate(ctx context.Context, templateID int, resolved *backendDocker.Template, userID *int) (*backendDocker.Template, error) {
	if resolved == nil {
		return s.fetchTemplateByID(ctx, templateID, userID)
	}
	if resolved.ID != 0 && resolved.ID != templateID {
		return nil, status.Wrap(fmt.Errorf("resolved template id does not match template_id"), status.InvalidArgument)
	}

	t := *resolved
	t.ID = templateID
	if err := validateContainerTemplate(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s APIService) fetchTemplateByID(ctx context.Context, templateID int, userID *int) (*backendDocker.Template, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/templates/%d", s.APIAddr, templateID), nil)
	if err != nil {
		return nil, status.Wrap(fmt.Errorf("create template request: %w", err), status.Internal)
	}

	req.Header.Set("Authorization", "Bearer "+s.MachineAuthData.MachineToken)
	if userID != nil && *userID > 0 {
		req.Header.Set("X-User-ID", fmt.Sprintf("%d", *userID))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, status.Wrap(fmt.Errorf("fetch template: %w", err), status.Internal)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, status.Wrap(fmt.Errorf("template not found"), status.NotFound)
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, status.Wrap(fmt.Errorf("fetch template failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), status.Internal)
	}

	var t backendDocker.Template
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, status.Wrap(fmt.Errorf("decode template: %w", err), status.Internal)
	}
	if err := validateContainerTemplate(&t); err != nil {
		return nil, err
	}

	return &t, nil
}

func validateContainerTemplate(t *backendDocker.Template) error {
	if t == nil {
		return status.Wrap(fmt.Errorf("template is required"), status.InvalidArgument)
	}
	if strings.TrimSpace(t.TemplateName) == "" {
		return status.Wrap(fmt.Errorf("template_name is required"), status.InvalidArgument)
	}
	if strings.TrimSpace(t.ImagePath) == "" {
		return status.Wrap(fmt.Errorf("image_path is required"), status.InvalidArgument)
	}
	if err := pkgdocker.ValidateTemplateLabels(*t); err != nil {
		return status.Wrap(err, status.InvalidArgument)
	}
	return nil
}
