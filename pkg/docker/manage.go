package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/antgpu/connector/internal/securefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	psDisk "github.com/shirou/gopsutil/v4/disk"
)

// StopContainer останавливает контейнер (с таймаутом)
func StopContainer(ctx context.Context, cli *client.Client, containerID string, timeout *time.Duration) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	var stopTimeout *int
	if timeout != nil {
		sec := int(timeout.Seconds())
		stopTimeout = &sec
	}
	if err := cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: stopTimeout}); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	return nil
}

// StartContainer запускает остановленный контейнер
func StartContainer(ctx context.Context, cli *client.Client, containerID string) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	return nil
}

// RestartContainer перезапускает контейнер
func RestartContainer(ctx context.Context, cli *client.Client, containerID string, timeout *time.Duration) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	var restartTimeout *int
	if timeout != nil {
		sec := int(timeout.Seconds())
		restartTimeout = &sec
	}
	if err := cli.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: restartTimeout}); err != nil {
		return fmt.Errorf("failed to restart container: %w", err)
	}
	return nil
}

// RemoveContainer удаляет контейнер (с опциями force и удаление томов)
func RemoveContainer(ctx context.Context, cli *client.Client, containerID string, force, removeVolumes bool) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}

	// Получаем информацию о контейнере, чтобы найти путь к скрипту
	containerInfo, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	// Удаляем контейнер
	removeOpts := container.RemoveOptions{
		Force:         force,
		RemoveVolumes: removeVolumes,
	}
	if err := cli.ContainerRemove(ctx, containerID, removeOpts); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	// Private receipts authorize host cleanup. Docker labels are descriptive;
	// legacy labels cannot establish ownership of a host file or image.
	if ownership, err := loadContainerOwnership(containerInfo.ID); err == nil {
		if ownership.ScriptPath != "" {
			if err := securefs.RemoveOwnedScript(resolveTemplateScriptsDir(), ownership.ScriptPath); err != nil {
				log.Printf("Warning: failed to remove bootstrap script: %v", err)
			}
		}
		evictManagedImageUnderPressure(ctx, cli, containerInfo, ownership)
		if path, err := ownershipPath(containerInfo.ID); err == nil {
			_ = os.Remove(path)
		}
	}

	return nil
}

func evictManagedImageUnderPressure(ctx context.Context, cli *client.Client, containerInfo container.InspectResponse, ownership containerOwnership) {
	if runtime.GOOS != "linux" || containerInfo.Config == nil {
		return
	}
	if ownership.ImageID != containerInfo.Image || !ownership.ImageOwned {
		return
	}
	labels := map[string]string{
		"antgpu.image_owned":                  "true",
		"antgpu.image_ref":                    ownership.ImageID,
		"antgpu.storage_safety_reserve_bytes": strconv.FormatUint(ownership.StorageReserve, 10),
	}
	info, err := cli.Info(ctx)
	if err != nil || strings.TrimSpace(info.DockerRootDir) == "" {
		return
	}
	usage, err := psDisk.Usage(info.DockerRootDir)
	if err != nil {
		return
	}
	imageRef, eligible := managedImageEvictionCandidate(labels, usage.Free)
	if !eligible {
		return
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("ancestor", imageRef)),
	})
	if err != nil || len(containers) > 0 {
		return
	}
	if _, err := cli.ImageRemove(ctx, imageRef, image.RemoveOptions{Force: false, PruneChildren: false}); err != nil {
		log.Printf("managed image eviction skipped for %s: %v", imageRef, err)
		return
	}
	log.Printf("evicted unused AntGPU-pulled image %s below host storage safety reserve", imageRef)
}

func managedImageEvictionCandidate(labels map[string]string, freeBytes uint64) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(labels["antgpu.image_owned"]), "true") {
		return "", false
	}
	reserveBytes, err := strconv.ParseUint(strings.TrimSpace(labels["antgpu.storage_safety_reserve_bytes"]), 10, 64)
	if err != nil || reserveBytes == 0 || freeBytes > reserveBytes {
		return "", false
	}
	imageRef := strings.TrimSpace(labels["antgpu.image_ref"])
	return imageRef, imageRef != ""
}

// InspectContainer возвращает детальную информацию о контейнере (аналог docker inspect)
func InspectContainer(ctx context.Context, cli *client.Client, containerID string) (container.InspectResponse, error) {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return container.InspectResponse{}, err
	}
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("failed to inspect container: %w", err)
	}
	return insp, nil
}

func InspectContainerWithSize(ctx context.Context, cli *client.Client, containerID string) (container.InspectResponse, error) {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return container.InspectResponse{}, err
	}
	insp, _, err := cli.ContainerInspectWithRaw(ctx, containerID, true)
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("failed to inspect container size: %w", err)
	}
	return insp, nil
}

// ContainerLogs возвращает поток логов контейнера
// follow: если true, читает логи в реальном времени; tail: количество строк от конца (например "100" или "all")
func ContainerLogs(ctx context.Context, cli *client.Client, containerID string, follow bool, tail string) (io.ReadCloser, error) {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return nil, err
	}
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	}
	logs, err := cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return nil, fmt.Errorf("failed to get container logs: %w", err)
	}
	return logs, nil
}

// ListContainers возвращает список контейнеров, управляемых приложением.
// all: если true, включает остановленные; filters: дополнительные фильтры (например, map[string]string{"status":"running"})
func ListContainers(ctx context.Context, cli *client.Client, all bool, extraFilters map[string]string) ([]container.Summary, error) {
	// Базовый фильтр по нашей метке
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "managed-by=template-manager")

	// Добавляем пользовательские фильтры
	for key, value := range extraFilters {
		filterArgs.Add(key, value)
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     all,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	return containers, nil
}

// UpdateContainerResources изменяет ресурсные ограничения контейнера на лету (CPU, память и т.д.)
func UpdateContainerResources(ctx context.Context, cli *client.Client, containerID string, resources container.Resources) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	if _, err := cli.ContainerUpdate(ctx, containerID, container.UpdateConfig{
		Resources: resources,
	}); err != nil {
		return fmt.Errorf("failed to update container resources: %w", err)
	}
	return nil
}

// PauseContainer приостанавливает все процессы в контейнере (используя cgroups freezer)
func PauseContainer(ctx context.Context, cli *client.Client, containerID string) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	if err := cli.ContainerPause(ctx, containerID); err != nil {
		return fmt.Errorf("failed to pause container: %w", err)
	}
	return nil
}

// ResumeContainer возобновляет выполнение приостановленного контейнера
func ResumeContainer(ctx context.Context, cli *client.Client, containerID string) error {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return err
	}
	if err := cli.ContainerUnpause(ctx, containerID); err != nil {
		return fmt.Errorf("failed to unpause container: %w", err)
	}
	return nil
}

// WaitContainer ожидает изменения состояния контейнера (остановка, удаление и т.д.)
// Возвращает канал с результатом ожидания и канал ошибок.
func WaitContainer(ctx context.Context, cli *client.Client, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	// Здесь не проверяем метку, потому что это асинхронная операция и контейнер может быть уже удалён.
	// Но можно проверить перед вызовом.
	return cli.ContainerWait(ctx, containerID, condition)
}

// Возвращает container.StatsResponseReader
// Это обёртка над:
// Body io.ReadCloser
// методом Close()
// Если stream = false → придёт один JSON
// Если stream = true → придёт бесконечный поток JSON объектов
// И важное: ты обязан закрыть reader, иначе словишь утечки.
func ContainerStatsStream(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	stream bool,
) (container.StatsResponseReader, error) {

	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return container.StatsResponseReader{}, err
	}

	stats, err := cli.ContainerStats(ctx, containerID, stream)
	if err != nil {
		return container.StatsResponseReader{}, fmt.Errorf("failed to get container stats: %w", err)
	}

	return stats, nil
}

func ContainerStatsText(
	ctx context.Context,
	cli *client.Client,
	containerID string,
) (string, error) {

	statsReader, err := ContainerStatsStream(ctx, cli, containerID, false)
	if err != nil {
		return "", err
	}
	defer statsReader.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(statsReader.Body).Decode(&stats); err != nil {
		return "", fmt.Errorf("failed to decode stats: %w", err)
	}

	// ---- CPU ----
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)

	var cpuPercent float64
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) *
			float64(len(stats.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}

	// ---- Memory ----
	memUsage := float64(stats.MemoryStats.Usage)
	memLimit := float64(stats.MemoryStats.Limit)

	memPercent := 0.0
	if memLimit > 0 {
		memPercent = (memUsage / memLimit) * 100.0
	}

	plaintext := fmt.Sprintf(
		"CPU: %.2f%%\nMemory: %.2f MB / %.2f MB (%.2f%%)\n",
		cpuPercent,
		memUsage/1024/1024,
		memLimit/1024/1024,
		memPercent,
	)

	return plaintext, nil
}

// AttachContainer подключается к контейнеру для интерактивного взаимодействия (например, для входа в shell)
// Опции можно настроить под конкретные нужды (stdin, stdout, stderr, stream, detach keys и т.д.)
func AttachContainer(ctx context.Context, cli *client.Client, containerID string, options container.AttachOptions) (types.HijackedResponse, error) {
	if err := ensureManagedContainer(ctx, cli, containerID); err != nil {
		return types.HijackedResponse{}, err
	}
	resp, err := cli.ContainerAttach(ctx, containerID, options)
	if err != nil {
		return types.HijackedResponse{}, fmt.Errorf("failed to attach to container: %w", err)
	}
	return resp, nil
}

func CheckStorageOptsSupport(ctx context.Context, cli *client.Client) (string, error) {
	if runtime.GOOS != "linux" {
		return "storage-opts size is supported only on Linux hosts (Docker Desktop uses a VM)", nil
	}

	info, err := cli.Info(ctx)
	if err != nil {
		return "unable to query Docker info", err
	}

	driver := strings.ToLower(info.Driver)

	backing := ""
	for _, kv := range info.DriverStatus {
		if len(kv) == 2 && strings.EqualFold(kv[0], "Backing Filesystem") {
			backing = strings.ToLower(kv[1])
			break
		}
	}

	switch driver {
	case "overlay2":
		if backing != "xfs" {
			return fmt.Sprintf("overlay2 backing filesystem is %q (requires xfs with pquota/prjquota)", backing), nil
		}
		hasQuota, quotaErr := mountHasProjectQuota(info.DockerRootDir)
		if quotaErr != nil {
			return "overlay2 project-quota mount options could not be verified", quotaErr
		}
		if !hasQuota {
			return "overlay2 uses xfs without a verified pquota/prjquota mount option", nil
		}
		return "", nil
	case "btrfs", "zfs":
		return "", nil
	default:
		return fmt.Sprintf("storage driver %q does not support size storage-opts", info.Driver), nil
	}
}

func mountHasProjectQuota(rootDir string) (bool, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return false, fmt.Errorf("docker root dir is empty")
	}
	if resolved, err := filepath.EvalSymlinks(rootDir); err == nil {
		rootDir = resolved
	}

	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	return mountInfoHasProjectQuota(string(raw), rootDir)
}

func mountInfoHasProjectQuota(raw, rootDir string) (bool, error) {
	bestMount := ""
	bestOptions := ""
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+3 >= len(fields) {
			continue
		}
		mountPoint := decodeMountInfoPath(fields[4])
		if rootDir != mountPoint && !strings.HasPrefix(rootDir, strings.TrimSuffix(mountPoint, "/")+"/") {
			continue
		}
		if len(mountPoint) < len(bestMount) {
			continue
		}
		bestMount = mountPoint
		bestOptions = fields[5] + "," + fields[separator+3]
	}
	if bestMount == "" {
		return false, fmt.Errorf("mount point for %s not found", rootDir)
	}
	for _, option := range strings.Split(strings.ToLower(bestOptions), ",") {
		if option == "pquota" || option == "prjquota" {
			return true, nil
		}
	}
	return false, nil
}

func decodeMountInfoPath(raw string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(raw)
}

// ensureManagedContainer проверяет, что контейнер помечен как управляемый нашим приложением
func ensureManagedContainer(ctx context.Context, cli *client.Client, containerID string) error {
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}
	if val, ok := insp.Config.Labels["managed-by"]; !ok || val != "template-manager" {
		return fmt.Errorf("container %s is not managed by this application", containerID)
	}
	return nil
}
