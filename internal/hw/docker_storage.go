package hw

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	pkgdocker "github.com/antgpu/connector/pkg/docker"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	psDisk "github.com/shirou/gopsutil/v4/disk"
)

type DockerStorageInfo struct {
	OSType          string    `json:"os_type,omitempty"`
	Driver          string    `json:"driver,omitempty"`
	RootDir         string    `json:"root_dir,omitempty"`
	TotalBytes      uint64    `json:"total_bytes,omitempty"`
	UsedBytes       uint64    `json:"used_bytes,omitempty"`
	FreeBytes       uint64    `json:"free_bytes,omitempty"`
	Source          string    `json:"source,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
	EnforcementMode string    `json:"enforcement_mode"`
	QuotaScope      string    `json:"quota_scope"`
	QuotaReason     string    `json:"quota_reason"`
	ImageLayerBytes int64     `json:"image_layer_bytes,omitempty"`
	BuildCacheBytes int64     `json:"build_cache_bytes,omitempty"`
	VolumeBytes     int64     `json:"volume_bytes,omitempty"`
	ModelCacheBytes int64     `json:"model_cache_bytes,omitempty"`
	Error           string    `json:"error"`
}

func FetchDockerStorageInfo() *DockerStorageInfo {
	return fetchDockerStorageInfo(true)
}

func FetchDockerStorageHeartbeat() *DockerStorageInfo {
	return fetchDockerStorageInfo(false)
}

func fetchDockerStorageInfo(includeCacheUsage bool) *DockerStorageInfo {

	out := &DockerStorageInfo{
		ObservedAt:      time.Now().UTC(),
		EnforcementMode: "unsupported",
		QuotaScope:      "writable_layer",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		out.Error = fmt.Sprintf("create docker client: %v", err)
		return out
	}
	defer cli.Close()

	info, err := cli.Info(ctx)
	if err != nil {
		out.Error = fmt.Sprintf("docker info: %v", err)
		return out
	}

	out.OSType = strings.TrimSpace(info.OSType)
	out.Driver = strings.TrimSpace(info.Driver)
	out.RootDir = strings.TrimSpace(info.DockerRootDir)

	probePath := out.RootDir
	out.Source = "docker_root_usage"
	if runtime.GOOS == "windows" && strings.EqualFold(out.OSType, "linux") && strings.HasPrefix(out.RootDir, "/") {
		sample, err := windowsDockerStorageUsage(cli.DaemonHost(), info.ID, info.KernelVersion)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.TotalBytes, out.FreeBytes, out.UsedBytes = sample.TotalBytes, sample.FreeBytes, sample.UsedBytes
		out.ObservedAt, out.Source = sample.ObservedAt, sample.Source
	} else if runtime.GOOS == "windows" {
		out.Source = "docker_root_windows_volume"
	} else {
		out.Source = "docker_root_statfs"
	}

	if strings.TrimSpace(probePath) == "" {
		out.Error = "docker root dir is empty"
		return out
	}

	if out.TotalBytes == 0 {
		usage, err := psDisk.Usage(probePath)
		if err != nil {
			out.Error = fmt.Sprintf("docker storage usage for %s: %v", probePath, err)
			return out
		}
		out.TotalBytes = usage.Total
		out.FreeBytes = usage.Free
		out.UsedBytes = usage.Used
	}

	warning, capabilityErr := pkgdocker.CheckStorageOptsSupport(ctx, cli)
	switch {
	case capabilityErr != nil:
		out.EnforcementMode = "soft"
		out.QuotaReason = fmt.Sprintf("hard quota check failed: %v", capabilityErr)
	case warning != "":
		out.EnforcementMode = "soft"
		out.QuotaReason = warning
	default:
		out.EnforcementMode = "hard"
	}
	if includeCacheUsage {
		if diskUsage, diskErr := cli.DiskUsage(ctx, types.DiskUsageOptions{}); diskErr == nil {
			out.ImageLayerBytes = max(diskUsage.LayersSize, 0)
			for _, record := range diskUsage.BuildCache {
				if record != nil && record.Size > 0 {
					out.BuildCacheBytes += record.Size
				}
			}
			for _, volume := range diskUsage.Volumes {
				if volume == nil || volume.UsageData == nil || volume.UsageData.Size <= 0 {
					continue
				}
				out.VolumeBytes += volume.UsageData.Size
				if strings.Contains(strings.ToLower(volume.Name), "model-runner") {
					out.ModelCacheBytes += volume.UsageData.Size
				}
			}
		}
	}
	return out
}

func dockerDesktopWSLDataPath() string {
	candidates := make([]string, 0, 8)
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Docker", "wsl", "disk", "docker_data.vhdx"),
			filepath.Join(localAppData, "Docker", "wsl", "data", "ext4.vhdx"),
		)
	}
	if runtime.GOOS == "windows" {
		if matches, err := filepath.Glob(`C:\Users\*\AppData\Local\Docker\wsl\disk\docker_data.vhdx`); err == nil {
			candidates = append(candidates, matches...)
		}
		if matches, err := filepath.Glob(`C:\Users\*\AppData\Local\Docker\wsl\data\ext4.vhdx`); err == nil {
			candidates = append(candidates, matches...)
		}
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	return ""
}
