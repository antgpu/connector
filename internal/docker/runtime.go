package docker

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
)

type RuntimeTarget struct {
	Kind     string
	URL      *url.URL
	Token    string
	Username string
	Password string
}

func (s *Service) ContainerRuntime(ctx context.Context, id string) (RuntimeTarget, error) {
	insp, err := s.InspectContainer(ctx, id)
	if err != nil {
		return RuntimeTarget{}, err
	}
	return runtimeTarget(insp)
}

func runtimeTarget(insp container.InspectResponse) (RuntimeTarget, error) {
	var target RuntimeTarget
	if insp.ContainerJSONBase == nil || insp.State == nil || !insp.State.Running || insp.Config == nil || insp.HostConfig == nil || insp.NetworkSettings == nil {
		return target, fmt.Errorf("runtime container is not running")
	}
	if insp.Config.Labels["managed-by"] != "template-manager" {
		return target, fmt.Errorf("runtime requires a managed container")
	}
	// Host/container network modes would let a template address another runtime.
	mode := string(insp.HostConfig.NetworkMode)
	if mode == "host" || strings.HasPrefix(mode, "container:") {
		return target, fmt.Errorf("runtime requires an isolated container network")
	}
	target.Kind = insp.Config.Labels["ai.antgpu.runtime"]
	if target.Kind != "unsloth" && target.Kind != "openai" {
		return target, fmt.Errorf("template has no supported runtime configured")
	}
	port, err := strconv.Atoi(insp.Config.Labels["ai.antgpu.runtime.port"])
	if err != nil || port < 1 || port > 65535 {
		return target, fmt.Errorf("template runtime port is missing or invalid")
	}
	// Resolve only the inspected container's private interface. Runtime ports need
	// not be published to the host or gateway. Connector runs beside its Docker daemon.
	names := make([]string, 0, len(insp.NetworkSettings.Networks))
	for name := range insp.NetworkSettings.Networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := insp.NetworkSettings.Networks[name]
		if n == nil {
			continue
		}
		ip := net.ParseIP(n.IPAddress)
		if ip == nil || !ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		target.URL = &url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), strconv.Itoa(port))}
		break
	}
	if target.URL == nil {
		return target, fmt.Errorf("runtime container has no private network address")
	}
	for _, entry := range insp.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "ANTGPU_RUNTIME_TOKEN":
			target.Token = value
		case "ANTGPU_UNSLOTH_USERNAME":
			target.Username = value
		case "ANTGPU_UNSLOTH_PASSWORD":
			target.Password = value
		}
	}
	return target, nil
}
