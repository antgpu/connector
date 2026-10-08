package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	nethttp "github.com/antgpu/connector/internal/apierror"
	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/antgpu/connector/internal/hw"
	"github.com/antgpu/connector/internal/updater"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/swaggest/openapi-go/openapi3"
	"github.com/swaggest/rest/response/gzip"
	"github.com/swaggest/rest/web"
	swgui "github.com/swaggest/swgui/v5emb"
	"github.com/swaggest/usecase"
	"github.com/swaggest/usecase/status"
)

type MachineAuthData struct {
	MachineID    string `json:"machine_id"`
	MachineToken string `json:"machine_token"`
}

type APIService struct {
	APIAddr           string
	MachineAuthData   MachineAuthData
	DockerService     *internaldocker.Service
	TunnelRuntime     *TunnelRuntime
	Updater           *updater.Manager
	OnConnectorRemove func(ConnectorRemoveInput)
	StateDir          string
	Provisioning      *ProvisioningManager
	UpdatesDisabled   bool
}

func (s APIService) WebService() *web.Service {
	webService := web.NewService(openapi3.NewReflector())

	webService.Wrap(
		s.machineTokenAuthMiddleware,
		gzip.Middleware,
		middleware.RequestID,
		middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: log.Default(), NoColor: true}),
		middleware.Recoverer,
	)

	webService.OpenAPISchema().SetTitle("AntGPU Client API")
	webService.OpenAPISchema().SetVersion("v0.0.1")

	if s.DockerService == nil {
		panic("api service requires DockerService dependency")
	}

	webService.Group(func(r chi.Router) {
		// Raw chi handlers do not inherit swaggest's Wrap middleware. Keep this
		// explicit: its gzip wrapper buffers SSE instead of flushing live events.
		r.Handle("/containers/{container_id}/runtime/*", s.machineTokenAuthMiddleware(http.HandlerFunc(s.ContainerRuntimeProxy)))
		r.Handle("/runtime/dmr/*", s.machineTokenAuthMiddleware(http.HandlerFunc(s.DMRRuntimeProxy)))
		createFromTemplateInteractor := usecase.NewInteractor(s.ContainerCreateFromTemplate)
		createFromTemplateInteractor.SetTags("Containers")
		createFromTemplateInteractor.SetTitle("Create and Start Container From Template")
		createFromTemplateInteractor.SetDescription("Creates and starts a managed container from a template. If another managed container is running, it is stopped first.")
		createFromTemplateInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/from-template", nethttp.NewHandler(createFromTemplateInteractor))

		createAsyncInteractor := usecase.NewInteractor(s.ContainerCreateFromTemplateAsync)
		createAsyncInteractor.SetTags("Containers")
		createAsyncInteractor.SetTitle("Provision Container From Template Asynchronously")
		r.Method(http.MethodPost, "/containers/from-template/async", nethttp.NewHandler(createAsyncInteractor))

		provisionStatusInteractor := usecase.NewInteractor(s.ContainerProvisioningStatus)
		provisionStatusInteractor.SetTags("Containers")
		provisionStatusInteractor.SetTitle("Get Container Provisioning Status")
		r.Method(http.MethodGet, "/containers/provisioning/{operation_id}", nethttp.NewHandler(provisionStatusInteractor))

		cancelProvisioningInteractor := usecase.NewInteractor(s.ContainerCancelProvisioning)
		cancelProvisioningInteractor.SetTags("Containers")
		cancelProvisioningInteractor.SetTitle("Cancel Container Provisioning")
		r.Method(http.MethodDelete, "/containers/provisioning/{operation_id}", nethttp.NewHandler(cancelProvisioningInteractor))

		startInteractor := usecase.NewInteractor(s.ContainerStart)
		startInteractor.SetTags("Containers")
		startInteractor.SetTitle("Start Container")
		startInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/start", nethttp.NewHandler(startInteractor))

		stopInteractor := usecase.NewInteractor(s.ContainerStop)
		stopInteractor.SetTags("Containers")
		stopInteractor.SetTitle("Stop Container")
		stopInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/stop", nethttp.NewHandler(stopInteractor))

		restartInteractor := usecase.NewInteractor(s.ContainerRestart)
		restartInteractor.SetTags("Containers")
		restartInteractor.SetTitle("Restart Container")
		restartInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/restart", nethttp.NewHandler(restartInteractor))

		pauseInteractor := usecase.NewInteractor(s.ContainerPause)
		pauseInteractor.SetTags("Containers")
		pauseInteractor.SetTitle("Pause Container")
		pauseInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/pause", nethttp.NewHandler(pauseInteractor))

		unpauseInteractor := usecase.NewInteractor(s.ContainerUnpause)
		unpauseInteractor.SetTags("Containers")
		unpauseInteractor.SetTitle("Unpause Container")
		unpauseInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/unpause", nethttp.NewHandler(unpauseInteractor))

		statsInteractor := usecase.NewInteractor(s.ContainerStats)
		statsInteractor.SetTags("Containers")
		statsInteractor.SetTitle("Container Stats")
		statsInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Internal,
		)
		r.Method(http.MethodGet, "/containers/{container_id}/stats", nethttp.NewHandler(statsInteractor))

		storageInteractor := usecase.NewInteractor(s.ContainerStorage)
		storageInteractor.SetTags("Containers")
		storageInteractor.SetTitle("Container Storage")
		storageInteractor.SetExpectedErrors(status.InvalidArgument, status.NotFound, status.Internal)
		r.Method(http.MethodGet, "/containers/{container_id}/storage", nethttp.NewHandler(storageInteractor))

		listInteractor := usecase.NewInteractor(s.ContainerList)
		listInteractor.SetTags("Containers")
		listInteractor.SetTitle("List Managed Containers")
		listInteractor.SetExpectedErrors(
			status.Internal,
		)
		r.Method(http.MethodGet, "/containers", nethttp.NewHandler(listInteractor))

		inspectInteractor := usecase.NewInteractor(s.ContainerInspect)
		inspectInteractor.SetTags("Containers")
		inspectInteractor.SetTitle("Inspect Container")
		inspectInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Internal,
		)
		r.Method(http.MethodGet, "/containers/{container_id}", nethttp.NewHandler(inspectInteractor))

		removeInteractor := usecase.NewInteractor(s.ContainerRemove)
		removeInteractor.SetTags("Containers")
		removeInteractor.SetTitle("Remove Container")
		removeInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Aborted,
			status.Internal,
		)
		r.Method(http.MethodDelete, "/containers/{container_id}", nethttp.NewHandler(removeInteractor))

		sshKeysInteractor := usecase.NewInteractor(s.ContainerSSHKeysApply)
		sshKeysInteractor.SetTags("Containers")
		sshKeysInteractor.SetTitle("Apply Container SSH Keys")
		sshKeysInteractor.SetDescription("Writes authorized_keys and enforces key-only SSH authentication.")
		sshKeysInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.FailedPrecondition,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/ssh-keys", nethttp.NewHandler(sshKeysInteractor))

		sshProbeInteractor := usecase.NewInteractor(s.ContainerSSHProbe)
		sshProbeInteractor.SetTags("Containers")
		sshProbeInteractor.SetTitle("Probe Container SSH Capability")
		sshProbeInteractor.SetDescription("Checks sshd capability and container SSH reachability on port 22.")
		sshProbeInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.NotFound,
			status.Internal,
		)
		r.Method(http.MethodPost, "/containers/{container_id}/ssh/probe", nethttp.NewHandler(sshProbeInteractor))

		r.Get("/containers/{container_id}/logs/ws", s.ContainerLogsWS)
		r.Get("/containers/{container_id}/shell/ws", s.ContainerShellWS)
	})

	webService.Group(func(r chi.Router) {
		applyMappingsInteractor := usecase.NewInteractor(s.TunnelPortMappingsApply)
		applyMappingsInteractor.SetTags("Tunnel")
		applyMappingsInteractor.SetTitle("Apply Tunnel Port Mappings")
		applyMappingsInteractor.SetDescription("Applies dynamic inbound tunnel port mappings without restarting connect.")
		applyMappingsInteractor.SetExpectedErrors(
			status.InvalidArgument,
			status.FailedPrecondition,
			status.Internal,
		)
		r.Method(http.MethodPost, "/tunnel/port-mappings", nethttp.NewHandler(applyMappingsInteractor))
	})

	webService.Group(func(r chi.Router) {
		statusInteractor := usecase.NewInteractor(s.ConnectorStatus)
		statusInteractor.SetTags("Connector")
		statusInteractor.SetTitle("Connector Runtime Status")
		statusInteractor.SetDescription("Returns connector version/state and update status.")
		statusInteractor.SetExpectedErrors(status.Internal)
		r.Method(http.MethodGet, "/connector/status", nethttp.NewHandler(statusInteractor))

		updateInteractor := usecase.NewInteractor(s.ConnectorUpdate)
		updateInteractor.SetTags("Connector")
		updateInteractor.SetTitle("Request Connector Update")
		updateInteractor.SetDescription("Requests self-update to the active backend version or explicit version.")
		updateInteractor.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodPost, "/connector/update", nethttp.NewHandler(updateInteractor))

		logsInteractor := usecase.NewInteractor(s.ConnectorLogs)
		logsInteractor.SetTags("Connector")
		logsInteractor.SetTitle("Read Connector Runtime Logs")
		logsInteractor.SetDescription("Reads connector runtime logs with optional since/tail filters.")
		logsInteractor.SetExpectedErrors(status.InvalidArgument, status.PermissionDenied, status.Internal)
		r.Method(http.MethodGet, "/connector/logs", nethttp.NewHandler(logsInteractor))

		removeInteractor := usecase.NewInteractor(s.ConnectorRemove)
		removeInteractor.SetTags("Connector")
		removeInteractor.SetTitle("Remove Connector Locally")
		removeInteractor.SetDescription("Best-effort local cleanup and connector shutdown after platform machine removal.")
		removeInteractor.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition)
		r.Method(http.MethodPost, "/connector/remove", nethttp.NewHandler(removeInteractor))
	})

	webService.Group(func(r chi.Router) {
		cfgStatus := usecase.NewInteractor(s.ConfigureStatus)
		cfgStatus.SetTags("Model Runner Configure")
		cfgStatus.SetTitle("Model Runner Availability Status")
		cfgStatus.SetDescription("Checks if Docker Model Runner API is reachable from connector runtime.")
		cfgStatus.SetExpectedErrors(status.Internal)
		r.Method(http.MethodGet, "/configure/status", nethttp.NewHandler(cfgStatus))
		// Compatibility aliases expected by current backend deployment flow.
		r.Method(http.MethodGet, "/model-runner/status", nethttp.NewHandler(cfgStatus))

		cfgPull := usecase.NewInteractor(s.ConfigurePullModel)
		cfgPull.SetTags("Model Runner Configure")
		cfgPull.SetTitle("Pull Model")
		cfgPull.SetDescription("Pulls a model into local Docker Model Runner cache.")
		cfgPull.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodPost, "/configure/models", nethttp.NewHandler(cfgPull))
		r.Method(http.MethodPost, "/model-runner/models", nethttp.NewHandler(cfgPull))

		cfgDelete := usecase.NewInteractor(s.ConfigureDeleteModel)
		cfgDelete.SetTags("Model Runner Configure")
		cfgDelete.SetTitle("Delete Model")
		cfgDelete.SetDescription("Deletes a model from local Docker Model Runner cache.")
		cfgDelete.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodDelete, "/configure/models", nethttp.NewHandler(cfgDelete))
		r.Method(http.MethodDelete, "/model-runner/models", nethttp.NewHandler(cfgDelete))

		cfgSet := usecase.NewInteractor(s.ConfigureModel)
		cfgSet.SetTags("Model Runner Configure")
		cfgSet.SetTitle("Configure Model Runtime Defaults")
		cfgSet.SetDescription("Runs `docker model configure` bridge for persistent model defaults.")
		cfgSet.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodPost, "/configure/model", nethttp.NewHandler(cfgSet))

		cfgReinstall := usecase.NewInteractor(s.ConfigureRunnerReinstall)
		cfgReinstall.SetTags("Model Runner Configure")
		cfgReinstall.SetTitle("Reinstall Model Runner Backend")
		cfgReinstall.SetDescription("Runs `docker model reinstall-runner` for one backend (llama.cpp|vllm|diffusers).")
		cfgReinstall.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodPost, "/configure/runner/reinstall", nethttp.NewHandler(cfgReinstall))

		cfgInspect := usecase.NewInteractor(s.ConfigureInspectModel)
		cfgInspect.SetTags("Model Runner Configure")
		cfgInspect.SetTitle("Inspect Model Config")
		cfgInspect.SetDescription("Returns `docker model inspect` output for one model.")
		cfgInspect.SetExpectedErrors(status.InvalidArgument, status.FailedPrecondition, status.Internal)
		r.Method(http.MethodGet, "/configure/model", nethttp.NewHandler(cfgInspect))

		r.HandleFunc("/engines/*", s.EnginesProxy)
		r.HandleFunc("/engines", s.EnginesProxy)
	})

	webService.Docs("/docs", swgui.New)

	return webService
}

func (s APIService) machineTokenAuthMiddleware(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(strings.TrimSpace(s.MachineAuthData.MachineToken)))
	configured := strings.TrimSpace(s.MachineAuthData.MachineToken) != ""

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model Runner inference is the rented workload data plane. The gateway
		// forwards a rent's public port to this path and cannot attach the
		// machine control token. Keep every control-plane route authenticated.
		if isModelRunnerInferencePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		gotToken := strings.TrimSpace(r.Header.Get("X-Machine-Token"))
		got := sha256.Sum256([]byte(gotToken))
		if !configured || gotToken == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "machine authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isModelRunnerInferencePath(path string) bool {
	return path == "/engines" || strings.HasPrefix(path, "/engines/")
}

func (s APIService) ReportHealth() error {
	type healthPayload struct {
		ConnectorVersion       string                `json:"connector_version,omitempty"`
		ConnectorStatus        string                `json:"connector_status,omitempty"`
		ConnectorTargetVersion string                `json:"connector_target_version,omitempty"`
		ConnectorLastError     string                `json:"connector_last_error,omitempty"`
		DockerStorage          *hw.DockerStorageInfo `json:"docker_storage,omitempty"`
	}

	payload := healthPayload{DockerStorage: hw.FetchDockerStorageHeartbeat()}
	if s.Updater != nil {
		snap := s.Updater.Snapshot()
		payload.ConnectorVersion = snap.Version
		payload.ConnectorStatus = snap.State
		payload.ConnectorTargetVersion = snap.TargetVersion
		payload.ConnectorLastError = snap.LastError
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal health payload: %w", err)
	}

	req, err := http.NewRequest("POST", s.APIAddr+"/machines/health", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.MachineAuthData.MachineToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

type ConnectorStatusInput struct{}

type ConnectorStatusOutput struct {
	Version       string `json:"version"`
	State         string `json:"state"`
	TargetVersion string `json:"target_version,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Updating      bool   `json:"updating"`
}

func (s APIService) ConnectorStatus(ctx context.Context, input ConnectorStatusInput, output *ConnectorStatusOutput) error {
	if s.Updater == nil {
		return status.Wrap(fmt.Errorf("updater is not configured"), status.FailedPrecondition)
	}

	snap := s.Updater.Snapshot()
	output.Version = snap.Version
	output.State = snap.State
	output.TargetVersion = snap.TargetVersion
	output.LastError = snap.LastError
	output.Updating = snap.State == string(updater.StateUpdating)

	return nil
}

type ConnectorUpdateInput struct {
	Version string `json:"version,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

func (s APIService) ConnectorUpdate(ctx context.Context, input ConnectorUpdateInput, output *updater.Result) error {
	if s.UpdatesDisabled {
		return status.Wrap(fmt.Errorf("connector updates are disabled for this build"), status.FailedPrecondition)
	}
	if s.Updater == nil {
		return status.Wrap(fmt.Errorf("updater is not configured"), status.FailedPrecondition)
	}

	result, err := s.Updater.RequestUpdate(ctx, updater.Request{
		Version: input.Version,
		Force:   input.Force,
	})
	if err != nil {
		return status.Wrap(fmt.Errorf("request update: %w", err), status.Internal)
	}

	*output = result
	return nil
}

type ConnectorRemoveInput struct {
	MachineID         string `json:"machine_id"`
	Reason            string `json:"reason"`
	RequestedByUserID int64  `json:"requested_by_user_id"`
}

type ConnectorRemoveOutput struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

func (s APIService) ConnectorRemove(ctx context.Context, input ConnectorRemoveInput, output *ConnectorRemoveOutput) error {
	machineID := strings.TrimSpace(input.MachineID)
	if machineID == "" {
		return status.Wrap(fmt.Errorf("machine_id is required"), status.InvalidArgument)
	}

	if localMachineID := strings.TrimSpace(s.MachineAuthData.MachineID); localMachineID != "" && localMachineID != machineID {
		return status.Wrap(fmt.Errorf("machine_id mismatch"), status.InvalidArgument)
	}

	if s.OnConnectorRemove == nil {
		return status.Wrap(fmt.Errorf("connector remove handler is not configured"), status.FailedPrecondition)
	}

	*output = ConnectorRemoveOutput{
		Accepted: true,
		Message:  "removal accepted",
	}

	req := input
	time.AfterFunc(250*time.Millisecond, func() {
		s.OnConnectorRemove(req)
	})

	return nil
}

func (s APIService) RefreshSpecs(reason string) error {
	if reason == "" {
		reason = "manual"
	}
	specs := hw.FetchSpecs()
	if err := s.SendSpecs(*specs); err != nil {
		return err
	}
	log.Printf("refreshed hardware specs (%s)", reason)
	return nil
}

func (s APIService) refreshSpecsAsync(reason string) {
	go func() {
		if err := s.RefreshSpecs(reason); err != nil {
			log.Printf("refresh hardware specs (%s): %v", reason, err)
		}
	}()
}

func (s APIService) SendSpecs(specs hw.SystemSpecs) error {
	specsBytes, err := json.Marshal(specs)
	if err != nil {
		return fmt.Errorf("marshal specs: %w", err)
	}
	req, err := http.NewRequest("POST", s.APIAddr+"/machines/specs", bytes.NewReader(specsBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.MachineAuthData.MachineToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
