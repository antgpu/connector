package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	pkgdocker "github.com/antgpu/connector/pkg/docker"
	backendDocker "github.com/antgpu/connector/pkg/protocol"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type RunningContainerInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type StopManagedContainersResult struct {
	Stopped  []RunningContainerInfo
	Warnings []string
}

type CreateFromTemplateResult struct {
	ContainerID string                `json:"container_id"`
	AutoStopped *RunningContainerInfo `json:"auto_stopped,omitempty"`
}

// PullProgress is re-exported for API callers that need image pull updates.
type PullProgress = pkgdocker.PullProgress

type ContainerStatsSnapshot struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	ReadAt        time.Time `json:"read_at"`

	CPUPercent float64 `json:"cpu_percent"`

	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	MemoryPercent    float64 `json:"memory_percent"`

	NetworkRxBytes uint64 `json:"network_rx_bytes"`
	NetworkTxBytes uint64 `json:"network_tx_bytes"`

	BlockReadBytes  uint64 `json:"block_read_bytes"`
	BlockWriteBytes uint64 `json:"block_write_bytes"`
}

type ContainerStorageSnapshot struct {
	ContainerID   string    `json:"container_id"`
	WritableBytes int64     `json:"writable_bytes"`
	RootFSBytes   int64     `json:"rootfs_bytes"`
	Running       bool      `json:"running"`
	ObservedAt    time.Time `json:"observed_at"`
}

type ShellSession struct {
	ExecID      string
	Hijacked    types.HijackedResponse
	closeClient func() error
}

func (s *ShellSession) Close() {
	s.Hijacked.Close()
	if s.closeClient != nil {
		_ = s.closeClient()
	}
}

type Service struct {
	newClient func() (*client.Client, error)
	runCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type AvailabilityStatus struct {
	DockerCLIAvailable bool
	DaemonAvailable    bool
	Error              string
}

func NewService() *Service {
	return &Service{
		newClient: func() (*client.Client, error) {
			return client.NewClientWithOpts(
				client.FromEnv,
				client.WithAPIVersionNegotiation(),
			)
		},
		runCmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		},
	}
}

func (s *Service) CheckAvailability(ctx context.Context) AvailabilityStatus {
	status := AvailabilityStatus{DockerCLIAvailable: true}
	if _, err := exec.LookPath("docker"); err != nil {
		status.DockerCLIAvailable = false
		status.Error = "Docker CLI not found. Install Docker and make sure the docker command is in PATH."
		return status
	}

	cli, err := s.newClient()
	if err != nil {
		status.Error = fmt.Sprintf("Docker daemon unreachable: %v. Start Docker and allow access to the Docker socket.", err)
		return status
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		status.Error = fmt.Sprintf("Docker daemon unreachable: %v. Start Docker and allow access to the Docker socket.", err)
		return status
	}

	status.DaemonAvailable = true
	return status
}

func (s *Service) ListManagedContainers(
	ctx context.Context,
	all bool,
	extraFilters map[string]string,
) ([]container.Summary, error) {
	cli, err := s.newClient()
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	items, err := pkgdocker.ListContainers(ctx, cli, all, extraFilters)
	if err != nil {
		return nil, fmt.Errorf("list managed containers: %w", err)
	}

	return items, nil
}

func runningManagedContainerFilters() map[string]string {
	return map[string]string{"status": "running"}
}

func (s *Service) HasRunningManagedContainers(ctx context.Context) (bool, error) {
	items, err := s.ListManagedContainers(ctx, false, runningManagedContainerFilters())
	if err != nil {
		return false, err
	}
	for _, c := range items {
		if c.State == "running" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) StopAllManagedContainers(ctx context.Context, timeout time.Duration) (StopManagedContainersResult, error) {
	var result StopManagedContainersResult
	running, err := s.ListManagedContainers(ctx, false, runningManagedContainerFilters())
	if err != nil {
		return result, err
	}

	for _, item := range running {
		if item.ID == "" || item.State != "running" {
			continue
		}
		if err := s.StopContainer(ctx, item.ID, &timeout); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("stop %s: %v", item.ID, err))
			continue
		}
		result.Stopped = append(result.Stopped, RunningContainerInfo{
			ID:    item.ID,
			Name:  normalizeContainerName(item.Names),
			State: "stopped",
		})
	}

	return result, nil
}

func (s *Service) CreateFromTemplate(
	ctx context.Context,
	t backendDocker.Template,
) (CreateFromTemplateResult, error) {
	return s.CreateFromTemplateWithProgress(ctx, t, nil)
}

func (s *Service) CreateFromTemplateWithProgress(
	ctx context.Context,
	t backendDocker.Template,
	onProgress func(pkgdocker.PullProgress),
) (CreateFromTemplateResult, error) {
	return s.CreateFromTemplateWithOptions(ctx, t, onProgress, pkgdocker.LaunchOptions{})
}

func (s *Service) CreateFromTemplateWithOptions(ctx context.Context, t backendDocker.Template, onProgress func(pkgdocker.PullProgress), options pkgdocker.LaunchOptions) (CreateFromTemplateResult, error) {
	var result CreateFromTemplateResult
	if err := pkgdocker.ValidateTemplateLabels(t); err != nil {
		return result, err
	}
	running, err := s.ListManagedContainers(ctx, false, map[string]string{"status": "running"})
	if err != nil {
		return result, fmt.Errorf("check running containers: %w", err)
	}

	if len(running) > 0 {
		var target container.Summary
		for _, c := range running {
			if c.State == "running" {
				target = c
				break
			}
		}
		if target.ID == "" {
			target = running[0]
		}

		stopTimeout := 20 * time.Second
		if err := s.StopContainer(ctx, target.ID, &stopTimeout); err != nil {
			return result, fmt.Errorf("auto-stop previous container %s: %w", target.ID, err)
		}

		result.AutoStopped = &RunningContainerInfo{
			ID:    target.ID,
			Name:  normalizeContainerName(target.Names),
			State: "stopped",
		}
	}

	containerID, err := pkgdocker.LaunchContainerWithOptions(ctx, t, onProgress, options)
	if err != nil {
		return result, fmt.Errorf("launch container from template: %w", err)
	}

	result.ContainerID = containerID

	return result, nil
}

func (s *Service) StartContainer(ctx context.Context, containerID string) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.StartContainer(ctx, cli, containerID); err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	return nil
}

func (s *Service) StopContainer(ctx context.Context, containerID string, timeout *time.Duration) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.StopContainer(ctx, cli, containerID, timeout); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}

	return nil
}

func (s *Service) RestartContainer(ctx context.Context, containerID string, timeout *time.Duration) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.RestartContainer(ctx, cli, containerID, timeout); err != nil {
		return fmt.Errorf("restart container: %w", err)
	}

	return nil
}

func (s *Service) PauseContainer(ctx context.Context, containerID string) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.PauseContainer(ctx, cli, containerID); err != nil {
		return fmt.Errorf("pause container: %w", err)
	}

	return nil
}

func (s *Service) UnpauseContainer(ctx context.Context, containerID string) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.ResumeContainer(ctx, cli, containerID); err != nil {
		return fmt.Errorf("unpause container: %w", err)
	}

	return nil
}

func (s *Service) RemoveContainer(ctx context.Context, containerID string, force bool, removeVolumes bool) error {
	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if err := pkgdocker.RemoveContainer(ctx, cli, containerID, force, removeVolumes); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}

	return nil
}

func (s *Service) InspectContainer(ctx context.Context, containerID string) (container.InspectResponse, error) {
	cli, err := s.newClient()
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	resp, err := pkgdocker.InspectContainer(ctx, cli, containerID)
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container: %w", err)
	}

	return resp, nil
}

func (s *Service) ContainerStorage(ctx context.Context, containerID string) (ContainerStorageSnapshot, error) {
	cli, err := s.newClient()
	if err != nil {
		return ContainerStorageSnapshot{}, fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	insp, err := pkgdocker.InspectContainerWithSize(ctx, cli, containerID)
	if err != nil {
		return ContainerStorageSnapshot{}, fmt.Errorf("inspect container storage: %w", err)
	}
	out := ContainerStorageSnapshot{ContainerID: containerID, ObservedAt: time.Now().UTC()}
	if insp.ContainerJSONBase != nil {
		if insp.SizeRw != nil && *insp.SizeRw > 0 {
			out.WritableBytes = *insp.SizeRw
		}
		if insp.SizeRootFs != nil && *insp.SizeRootFs > 0 {
			out.RootFSBytes = *insp.SizeRootFs
		}
		out.Running = insp.State != nil && insp.State.Running
	}
	return out, nil
}

func (s *Service) Logs(
	ctx context.Context,
	containerID string,
	follow bool,
	tail string,
) (io.ReadCloser, error) {
	cli, err := s.newClient()
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	logs, err := pkgdocker.ContainerLogs(ctx, cli, containerID, follow, tail)
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("container logs: %w", err)
	}

	return &compositeReadCloser{
		reader: logs,
		closeFn: func() error {
			logs.Close()
			return cli.Close()
		},
	}, nil
}

func (s *Service) StatsSnapshot(ctx context.Context, containerID string) (ContainerStatsSnapshot, error) {
	cli, err := s.newClient()
	if err != nil {
		return ContainerStatsSnapshot{}, fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	statsReader, err := pkgdocker.ContainerStatsStream(ctx, cli, containerID, false)
	if err != nil {
		return ContainerStatsSnapshot{}, fmt.Errorf("stats stream: %w", err)
	}
	defer statsReader.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(statsReader.Body).Decode(&stats); err != nil {
		return ContainerStatsSnapshot{}, fmt.Errorf("decode stats response: %w", err)
	}

	snap := ContainerStatsSnapshot{
		ContainerID:      stats.ID,
		ContainerName:    strings.TrimPrefix(stats.Name, "/"),
		ReadAt:           stats.Read,
		MemoryUsageBytes: stats.MemoryStats.Usage,
		MemoryLimitBytes: stats.MemoryStats.Limit,
	}

	snap.CPUPercent = cpuPercent(stats)
	if snap.MemoryLimitBytes > 0 {
		snap.MemoryPercent = float64(snap.MemoryUsageBytes) / float64(snap.MemoryLimitBytes) * 100
	}

	for _, n := range stats.Networks {
		snap.NetworkRxBytes += n.RxBytes
		snap.NetworkTxBytes += n.TxBytes
	}

	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			snap.BlockReadBytes += entry.Value
		case "write":
			snap.BlockWriteBytes += entry.Value
		}
	}

	return snap, nil
}

func (s *Service) OpenShellSession(ctx context.Context, containerID string, tty bool) (*ShellSession, error) {
	cli, err := s.newClient()
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	if _, err := pkgdocker.InspectContainer(ctx, cli, containerID); err != nil {
		cli.Close()
		return nil, fmt.Errorf("inspect managed container: %w", err)
	}

	cmd, err := selectContainerShell(ctx, cli, containerID)
	if err != nil {
		cli.Close()
		return nil, err
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          tty,
		Cmd:          cmd,
	})
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("create shell exec: %w", err)
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{
		Tty: tty,
	})
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("attach shell exec: %w", err)
	}

	return &ShellSession{
		ExecID: execResp.ID,
		Hijacked: types.HijackedResponse{
			Conn:   hijacked.Conn,
			Reader: hijacked.Reader,
		},
		closeClient: cli.Close,
	}, nil
}

type shellCommand struct {
	Path  string
	Login bool
}

func shellCandidates() []shellCommand {
	paths := []string{
		"/bin/bash",
		"/bin/sh",
		"/bin/ash",
		"/usr/bin/bash",
		"/usr/bin/sh",
		"/usr/bin/ash",
		"/bin/zsh",
		"/usr/bin/zsh",
		"/bin/fish",
		"/usr/bin/fish",
	}

	candidates := make([]shellCommand, 0, len(paths)*2)
	for _, path := range paths {
		candidates = append(candidates, shellCommand{Path: path, Login: true})
	}
	for _, path := range paths {
		candidates = append(candidates, shellCommand{Path: path})
	}
	return candidates
}

func (c shellCommand) execCmd() []string {
	if c.Login {
		return []string{c.Path, "-l"}
	}
	return []string{c.Path}
}

func (c shellCommand) probeCmd() []string {
	if c.Login {
		return []string{c.Path, "-l", "-c", "exit 0"}
	}
	return []string{c.Path, "-c", "exit 0"}
}

func selectContainerShell(ctx context.Context, cli *client.Client, containerID string) ([]string, error) {
	var failures []string
	for _, candidate := range shellCandidates() {
		exitCode, output, err := execInContainer(ctx, cli, containerID, candidate.probeCmd())
		if err == nil && exitCode == 0 {
			return candidate.execCmd(), nil
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", strings.Join(candidate.execCmd(), " "), err))
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: exit %d: %s", strings.Join(candidate.execCmd(), " "), exitCode, strings.TrimSpace(output)))
	}
	return nil, fmt.Errorf("no supported shell found in container (%s)", strings.Join(failures, "; "))
}

type compositeReadCloser struct {
	reader  io.ReadCloser
	closeFn func() error
}

func (c *compositeReadCloser) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *compositeReadCloser) Close() error {
	if c.closeFn != nil {
		return c.closeFn()
	}
	return nil
}

func normalizeContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func cpuPercent(stats container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs <= 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta <= 0 || cpuDelta <= 0 || onlineCPUs <= 0 {
		return 0
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100
}
