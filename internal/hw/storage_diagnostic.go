package hw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// This small, immutable official Alpine image supplies only statfs. An opted-in
// canary may download it once; admission/heartbeat measurements are unchanged.
const storageDiagnosticImage = "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"

type StorageDiagnostic struct {
	ObservedAt time.Time      `json:"observed_at"`
	Docker     map[string]any `json:"docker"`
	Host       any            `json:"host"`
	Guest      map[string]any `json:"guest"`
}

func CollectStorageDiagnostic(ctx context.Context) StorageDiagnostic {
	out := StorageDiagnostic{ObservedAt: time.Now().UTC(), Docker: map[string]any{}, Guest: map[string]any{}}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		out.Docker["error"] = "client_creation_failed"
		return out
	}
	defer cli.Close()
	info, err := cli.Info(ctx)
	if err != nil {
		out.Docker["error"] = "info_unavailable"
	} else {
		out.Docker = map[string]any{"server_version": info.ServerVersion, "driver": info.Driver,
			"kernel_version": info.KernelVersion, "operating_system": info.OperatingSystem,
			"os_type": info.OSType, "docker_root": info.DockerRootDir, "driver_status": info.DriverStatus}
		if info.OSType == "linux" {
			out.Guest = probeStorageDiagnosticGuest(ctx, cli, true)
		} else {
			out.Guest["error"] = "not_a_linux_engine"
		}
	}
	// A disconnected disk or a slow Windows profile must not hold the workload
	// mutex or delay connector shutdown beyond this diagnostic's deadline.
	host := make(chan any, 1)
	go func() { host <- collectStorageDiagnosticHost(ctx) }()
	select {
	case out.Host = <-host:
	case <-ctx.Done():
		out.Host = map[string]string{"status": "host_probe_timeout"}
	}
	return out
}

func storageDiagnosticHostConfig() *container.HostConfig {
	pids := int64(16)
	return &container.HostConfig{
		NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Resources:   container.Resources{Memory: 32 << 20, MemorySwap: 32 << 20, NanoCPUs: 100_000_000, PidsLimit: &pids},
		LogConfig:   container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "8k", "max-file": "1"}},
	}
}

func probeStorageDiagnosticGuest(parent context.Context, cli *client.Client, allowPull bool) map[string]any {
	out := map[string]any{"scope": "container_root_filesystem"}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	inspected, err := cli.ImageInspect(ctx, storageDiagnosticImage)
	if allowPull && errdefs.IsNotFound(err) {
		pull, pullErr := cli.ImagePull(ctx, storageDiagnosticImage, image.PullOptions{})
		if pullErr != nil {
			out["error"] = "pinned_helper_pull_failed"
			return out
		}
		_, pullErr = io.Copy(io.Discard, io.LimitReader(pull, 1<<20))
		pull.Close()
		if pullErr != nil || ctx.Err() != nil {
			out["error"] = "pinned_helper_pull_incomplete"
			return out
		}
		inspected, err = cli.ImageInspect(ctx, storageDiagnosticImage)
	}
	if err != nil {
		out["error"] = "pinned_helper_not_cached"
		return out
	}
	verified := false
	for _, digest := range inspected.RepoDigests {
		if strings.HasSuffix(digest, storageDiagnosticImage[strings.Index(storageDiagnosticImage, "@"):]) {
			verified = true
		}
	}
	if !verified || !strings.HasPrefix(inspected.ID, "sha256:") {
		out["error"] = "helper_digest_mismatch"
		return out
	}
	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: inspected.ID, User: "65534:65534", Entrypoint: []string{"/bin/sh"},
		Cmd:    []string{"-ec", "stat -f -c '%S %b %f %a' /"},
		Labels: map[string]string{"antgpu.helper": "storage-diagnostic"},
	}, storageDiagnosticHostConfig(), nil, nil, "")
	if err != nil {
		out["error"] = "helper_create_failed"
		return out
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cli.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true}); err != nil {
			out["cleanup_error"] = "helper_remove_failed"
		}
	}()
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		out["error"] = "helper_start_failed"
		return out
	}
	wait, failed := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case <-ctx.Done():
		out["error"] = "helper_timeout"
		return out
	case <-failed:
		out["error"] = "helper_wait_failed"
		return out
	case result := <-wait:
		if result.StatusCode != 0 || result.Error != nil {
			out["error"] = "helper_nonzero_exit"
			return out
		}
	}
	logs, err := cli.ContainerLogs(ctx, created.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		out["error"] = "helper_logs_failed"
		return out
	}
	defer logs.Close()
	var stdout, stderr bytes.Buffer
	if _, err = stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(logs, 4096)); err != nil || stderr.Len() != 0 {
		out["error"] = "helper_output_invalid"
		return out
	}
	counts, err := parseStorageDiagnosticStat(stdout.String())
	if err != nil {
		out["error"] = "helper_output_invalid"
		return out
	}
	for key, value := range counts {
		out[key] = value
	}
	return out
}

func parseStorageDiagnosticStat(raw string) (map[string]uint64, error) {
	fields := strings.Fields(raw)
	if len(fields) != 4 {
		return nil, fmt.Errorf("expected four statfs counts")
	}
	var values [4]uint64
	for i := range values {
		n, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid statfs count")
		}
		values[i] = n
	}
	if values[0] == 0 || values[1] == 0 || values[2] > values[1] || values[3] > values[2] || values[1] > math.MaxUint64/values[0] {
		return nil, fmt.Errorf("inconsistent statfs counts")
	}
	return map[string]uint64{"total_bytes": values[0] * values[1], "free_bytes": values[0] * values[2], "available_bytes": values[0] * values[3]}, nil
}

// Keep diagnostic log payloads bounded even when many profiles are installed.
func StorageDiagnosticFields(report StorageDiagnostic) map[string]any {
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 48*1024 {
		return map[string]any{"error": "diagnostic_report_too_large"}
	}
	return map[string]any{"report": report}
}
