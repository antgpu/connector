package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	docker "github.com/antgpu/connector-protocol"
	"github.com/antgpu/connector/internal/securefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
)

type templatePortMapping struct {
	hostPort      string
	containerPort string
	protocols     []string
}

// PullProgress is a normalized Docker image-pull update. Totals are only
// available once Docker has announced them for a layer.
type PullProgress struct {
	Status  string
	LayerID string
	Current int64
	Total   int64
}

const templateScriptsDirEnv = "CONNECT_TEMPLATE_SCRIPTS_DIR"

func resolveTemplateScriptsDir() string {
	if v := strings.TrimSpace(os.Getenv(templateScriptsDirEnv)); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" {
			return filepath.Join(pd, "Antgpu", "Connect", "template-manager", "scripts")
		}
	}
	return filepath.Join("cache", "scripts")
}

func sanitizeContainerName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	mapped := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, raw)

	mapped = strings.Trim(mapped, "-_.")
	if mapped == "" {
		return ""
	}

	first := rune(mapped[0])
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		mapped = "c" + mapped
	}
	return mapped
}

// drainPullProgress consumes Docker's pull stream, forwarding normalized
// progress. It returns an error if the daemon reports one mid-stream, which
// ImagePull itself cannot surface because it returns before the transfer runs.
func drainPullProgress(out io.ReadCloser, onProgress func(PullProgress)) error {
	decoder := json.NewDecoder(out)
	for {
		var event struct {
			Status         string `json:"status"`
			ID             string `json:"id"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read image pull progress: %w", err)
		}
		if event.Error != "" {
			return fmt.Errorf("image pull: %s", event.Error)
		}
		if onProgress != nil {
			onProgress(PullProgress{
				Status:  event.Status,
				LayerID: event.ID,
				Current: event.ProgressDetail.Current,
				Total:   event.ProgressDetail.Total,
			})
		}
	}
}

// LaunchContainer creates and starts a container without reporting pull state.
func LaunchContainer(ctx context.Context, t docker.Template) (string, error) {
	return LaunchContainerWithProgress(ctx, t, nil)
}

// LaunchContainerWithProgress creates and starts a container while forwarding
// Docker pull progress. The callback must return quickly.
type LaunchOptions struct {
	RentID                    string
	StorageSafetyReserveBytes uint64
}

func LaunchContainerWithProgress(ctx context.Context, t docker.Template, onProgress func(PullProgress)) (string, error) {
	return LaunchContainerWithOptions(ctx, t, onProgress, LaunchOptions{})
}

func LaunchContainerWithOptions(ctx context.Context, t docker.Template, onProgress func(PullProgress), options LaunchOptions) (string, error) {
	labels, optEnv, err := templateLabels(t)
	if err != nil {
		return "", err
	}

	// 1. Создаём клиент Docker
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()
	hostConfig := &container.HostConfig{StorageOpt: make(map[string]string)}
	if err := configureWorkload(ctx, cli, hostConfig); err != nil {
		return "", err
	}
	if err := configureWorkloadStorage(ctx, cli, hostConfig, t.DiskSpaceMB); err != nil {
		return "", err
	}

	// 2. Формируем полное имя образа (с учётом DockerServerName)
	imageName := t.ImagePath
	if t.DockerServerName != "" && !strings.Contains(t.ImagePath, "/") {
		// Если сервер указан и путь не содержит слеша, добавляем префикс
		imageName = t.DockerServerName + "/" + t.ImagePath
	}
	_, _, imageInspectErr := cli.ImageInspectWithRaw(ctx, imageName)
	imageOwned := errdefs.IsNotFound(imageInspectErr)

	// 3. Аутентификация для pull (если есть логин/пароль)
	var authConfig registry.AuthConfig
	if t.DockerUsername != "" && t.DockerPassword != "" {
		authConfig = registry.AuthConfig{
			Username:      t.DockerUsername,
			Password:      t.DockerPassword,
			ServerAddress: t.DockerServerName,
		}
	}
	authStr := ""
	if authConfig.Username != "" {
		encodedJSON, err := json.Marshal(authConfig)
		if err != nil {
			return "", fmt.Errorf("failed to marshal auth config: %w", err)
		}
		authStr = base64.URLEncoding.EncodeToString(encodedJSON)
	}

	// 4. Pull образа.
	// Ошибка pull фатальна только если образа нет локально. Провайдер может
	// собрать большой образ (CUDA + torch — десятки гигабайт) прямо на своей
	// машине, и тогда тянуть его через реестр бессмысленно. Так же ведёт себя
	// временно недоступный реестр: рабочий локальный образ лучше отказа.
	out, err := cli.ImagePull(ctx, imageName, image.PullOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		if _, inspectErr := cli.ImageInspect(ctx, imageName); inspectErr != nil {
			return "", fmt.Errorf("failed to pull image and no local copy: %w", err)
		}
		fmt.Printf("Warning: could not pull %s (%v); using the local copy instead.\n", imageName, err)
		out = nil
	}
	if out != nil {
		defer out.Close()
	}
	if out != nil {
		if err := drainPullProgress(out, onProgress); err != nil {
			return "", err
		}
	}
	labels["template-name"] = t.TemplateName
	labels["managed-by"] = "template-manager"
	labels["antgpu.image_ref"] = imageName
	if imageOwned {
		labels["antgpu.image_owned"] = "true"
	}
	if options.RentID != "" {
		labels["antgpu.rent_id"] = options.RentID
	}
	if options.StorageSafetyReserveBytes > 0 {
		labels["antgpu.storage_safety_reserve_bytes"] = strconv.FormatUint(options.StorageSafetyReserveBytes, 10)
	}
	env := append(append([]string(nil), t.EnvironmentVariables...), optEnv...)
	env, err = provisionRuntimeCredentials(labels, env)
	if err != nil {
		return "", err
	}
	// 7. Обработка OnStartScript: создаём постоянный файл
	var cmd []string
	var entrypoint []string
	var binds []string
	var scriptHostPath string

	if t.OnStartScript != "" && runtime.GOOS != "windows" {
		scriptsDir := resolveTemplateScriptsDir()
		if err := securefs.PrivateDir(scriptsDir); err != nil {
			return "", fmt.Errorf("secure scripts directory: %w", err)
		}
		script, err := os.CreateTemp(scriptsDir, "bootstrap-*.sh")
		if err != nil {
			return "", fmt.Errorf("create bootstrap script: %w", err)
		}
		scriptHostPath, err = filepath.Abs(script.Name())
		if err != nil {
			script.Close()
			os.Remove(script.Name())
			return "", err
		}
		if _, err = script.WriteString(t.OnStartScript); err == nil {
			err = script.Chmod(0o700)
		}
		closeErr := script.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(scriptHostPath)
			return "", fmt.Errorf("write bootstrap script: %w", err)
		}

		// Добавляем метку с путём к скрипту для последующего удаления
		labels["onstart-script-path"] = scriptHostPath

		// Bind mount: файл хоста -> /onstart.sh внутри контейнера (read-only)
		binds = append(binds, fmt.Sprintf("%s:/onstart.sh:ro", scriptHostPath))

		// Override image entrypoint to run bootstrap script first.
		entrypoint = []string{"/bin/sh", "/onstart.sh"}
		// Если нужно, чтобы после скрипта запускался оригинальный процесс,
		// скрипт должен сам вызвать exec (например, добавив в конце exec "$@").
	}
	if t.OnStartScript != "" && runtime.GOOS == "windows" {
		// Docker Desktop's file-sharing process uses the desktop token, which
		// must not gain access to the privileged connector's private state.
		// Copy into this container instead of publishing a host script path.
		entrypoint = []string{"/bin/sh", "/onstart.sh"}
	}

	// 8. Проброс портов
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}
	for _, p := range t.Ports {
		mapping, parseErr := parseTemplatePortMapping(p)
		if parseErr != nil {
			fmt.Printf("Warning: invalid port mapping %q: %v. Skipping.\n", p, parseErr)
			continue
		}

		for _, proto := range mapping.protocols {
			port, err := nat.NewPort(proto, mapping.containerPort)
			if err != nil {
				continue
			}
			exposedPorts[port] = struct{}{}
			portBindings[port] = []nat.PortBinding{
				{
					HostIP:   "127.0.0.1",
					HostPort: mapping.hostPort,
				},
			}
		}
	}
	ensureSSHLoopbackPortBinding(exposedPorts, portBindings)

	// 9. Настройки HostConfig
	hostConfig.PortBindings = portBindings
	hostConfig.Binds = binds
	configureGPU(ctx, cli, hostConfig, t.VRAMRequiredGB)

	// 10. Основная конфигурация контейнера
	config := &container.Config{
		Image:        imageName,
		Env:          env,
		Labels:       labels,
		Cmd:          cmd,
		Entrypoint:   entrypoint,
		ExposedPorts: exposedPorts,
		// Если нужно изменить Entrypoint, можно задать здесь
		// Entrypoint: []string{...}
	}

	// 11. Создание контейнера
	// Имя контейнера берётся из TemplateName (если пустое, Docker сгенерирует сам)
	resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		// В случае ошибки создания контейнера удаляем созданный скрипт
		if scriptHostPath != "" {
			os.Remove(scriptHostPath)
		}
		return "", fmt.Errorf("failed to create container: %w", err)
	}
	if t.OnStartScript != "" && runtime.GOOS == "windows" {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		err := tw.WriteHeader(&tar.Header{Name: "onstart.sh", Mode: 0o500, Size: int64(len(t.OnStartScript))})
		if err == nil {
			_, err = tw.Write([]byte(t.OnStartScript))
		}
		closeErr := tw.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = cli.CopyToContainer(ctx, resp.ID, "/", &archive, container.CopyToContainerOptions{AllowOverwriteDirWithFile: false})
		}
		if err != nil {
			_ = cli.ContainerRemove(context.WithoutCancel(ctx), resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			return "", fmt.Errorf("copy container bootstrap: %w", err)
		}
	}

	// 12. Запуск контейнера
	if err := startIsolatedWorkload(ctx, cli, resp.ID); err != nil {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		// В случае ошибки запуска удаляем скрипт
		if scriptHostPath != "" {
			os.Remove(scriptHostPath)
		}
		return "", fmt.Errorf("failed to start container: %w", err)
	}
	if err := recordContainerOwnership(ctx, cli, resp.ID, imageName, imageOwned, scriptHostPath, options.StorageSafetyReserveBytes); err != nil {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if scriptHostPath != "" {
			_ = securefs.RemoveOwnedScript(resolveTemplateScriptsDir(), scriptHostPath)
		}
		return "", fmt.Errorf("record container ownership: %w", err)
	}

	fmt.Printf("Container %s started with ID %s\n", t.TemplateName, resp.ID)
	return resp.ID, nil
}

// configureGPU gives GPU-required templates access to every NVIDIA GPU exposed
// by the provider's Docker daemon. CPU templates must remain unchanged.
//
// DeviceRequests is what actually attaches the GPUs — it is exactly what
// `docker run --gpus all` sends. The named "nvidia" runtime is a second,
// optional thing: the NVIDIA Container Toolkit registers it on a native Linux
// daemon, but Docker Desktop (Windows/macOS, WSL2 backend) exposes GPUs through
// DeviceRequests alone and registers no such runtime. Naming a runtime the
// daemon does not have makes ContainerCreate fail outright with "Unknown runtime
// specified nvidia", so on those hosts every GPU template was unprovisionable
// while CPU templates worked — the failure looked like a broken template rather
// than a missing runtime.
//
// So the runtime is now requested only when the daemon reports it. Dropping it
// where it is absent costs nothing: DeviceRequests carries the GPUs either way.
func configureGPU(ctx context.Context, cli *client.Client, hostConfig *container.HostConfig, vramRequiredGB int) {
	if vramRequiredGB <= 0 {
		return
	}

	hostConfig.Resources.DeviceRequests = []container.DeviceRequest{{
		Driver:       "nvidia",
		Count:        -1,
		Capabilities: [][]string{{"gpu"}},
	}}

	if hasNvidiaRuntime(ctx, cli) {
		hostConfig.Runtime = "nvidia"
		return
	}
	fmt.Println("Info: Docker daemon reports no \"nvidia\" runtime; " +
		"attaching GPUs via device requests only (normal on Docker Desktop).")
}

// hasNvidiaRuntime reports whether the daemon has a runtime named "nvidia".
//
// Failing closed on a query error is deliberate: a container created without the
// runtime still gets its GPUs, whereas one created with a runtime the daemon
// lacks does not get created at all.
func hasNvidiaRuntime(ctx context.Context, cli *client.Client) bool {
	info, err := cli.Info(ctx)
	if err != nil {
		fmt.Printf("Warning: could not query Docker runtimes (%v); "+
			"attaching GPUs via device requests only.\n", err)
		return false
	}
	for name := range info.Runtimes {
		if strings.EqualFold(name, "nvidia") {
			return true
		}
	}
	return false
}

func ensureSSHLoopbackPortBinding(exposedPorts nat.PortSet, portBindings nat.PortMap) {
	sshPort, err := nat.NewPort("tcp", "22")
	if err != nil {
		return
	}
	if _, ok := portBindings[sshPort]; ok {
		return
	}
	exposedPorts[sshPort] = struct{}{}
	portBindings[sshPort] = []nat.PortBinding{{HostIP: "127.0.0.1"}}
}

func parseDockerOptions(options string) (envVars []string, extraLabels map[string]string, err error) {
	extraLabels = make(map[string]string)
	if options == "" {
		return
	}

	// Разбиваем строку по пробелам (упрощённо)
	parts := strings.Fields(options)
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		switch part {
		case "-e", "--env":
			if i+1 >= len(parts) {
				err = fmt.Errorf("missing argument for %s", part)
				return
			}
			envVars = append(envVars, parts[i+1])
			i++ // пропускаем аргумент
		case "-l", "--label":
			if i+1 >= len(parts) {
				err = fmt.Errorf("missing argument for %s", part)
				return
			}
			labelPair := parts[i+1]
			kv := strings.SplitN(labelPair, "=", 2)
			if len(kv) == 2 {
				extraLabels[kv[0]] = kv[1]
			} else {
				extraLabels[kv[0]] = "" // если нет значения
			}
			i++
		default:
			// Пропускаем неизвестные опции (или можно вернуть ошибку)
			// Например: --shm-size, --ulimit и т.п. пока не поддерживаются
		}
	}
	return
}

func parseTemplatePortMapping(raw string) (templatePortMapping, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return templatePortMapping{}, fmt.Errorf("empty port mapping")
	}

	portSpec := spec
	protocolSpec := ""
	if idx := strings.LastIndex(spec, "/"); idx >= 0 {
		portSpec = strings.TrimSpace(spec[:idx])
		protocolSpec = strings.ToLower(strings.TrimSpace(spec[idx+1:]))
	}

	protocols, err := parsePortProtocols(protocolSpec)
	if err != nil {
		return templatePortMapping{}, err
	}

	parts := strings.Split(portSpec, ":")
	var hostPort string
	var containerPort string
	switch len(parts) {
	case 1:
		containerPort = strings.TrimSpace(parts[0])
	case 2:
		hostPort = strings.TrimSpace(parts[0])
		containerPort = strings.TrimSpace(parts[1])
	default:
		return templatePortMapping{}, fmt.Errorf("invalid format, expected containerPort or hostPort:containerPort with optional /proto suffix")
	}

	if containerPort == "" {
		return templatePortMapping{}, fmt.Errorf("container port is empty")
	}
	if err := validatePortNumber(containerPort); err != nil {
		return templatePortMapping{}, fmt.Errorf("invalid container port %q: %w", containerPort, err)
	}
	if hostPort != "" {
		if err := validatePortNumber(hostPort); err != nil {
			return templatePortMapping{}, fmt.Errorf("invalid host port %q: %w", hostPort, err)
		}
	} else {
		// Keep host mapping deterministic so tunnel forwarding can target a stable local port.
		hostPort = containerPort
	}

	return templatePortMapping{
		hostPort:      hostPort,
		containerPort: containerPort,
		protocols:     protocols,
	}, nil
}

func parsePortProtocols(raw string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		// Docker defaults to TCP when protocol is omitted in the port spec.
		return []string{"tcp"}, nil
	case "tcp":
		return []string{"tcp"}, nil
	case "udp":
		return []string{"udp"}, nil
	case "both":
		return []string{"tcp", "udp"}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q (allowed: tcp, udp, both)", raw)
	}
}

func validatePortNumber(raw string) error {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("out of range")
	}
	return nil
}
