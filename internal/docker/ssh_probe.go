package docker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	pkgdocker "github.com/antgpu/connector/pkg/docker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

type ContainerSSHProbeResult struct {
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	ContainerIP string `json:"container_ip,omitempty"`
	Port        int    `json:"port,omitempty"`
}

func (s *Service) ProbeContainerSSH(ctx context.Context, containerID string) (ContainerSSHProbeResult, error) {
	cli, err := s.newClient()
	if err != nil {
		return ContainerSSHProbeResult{}, fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	insp, err := pkgdocker.InspectContainer(ctx, cli, containerID)
	if err != nil {
		return ContainerSSHProbeResult{}, fmt.Errorf("inspect managed container: %w", err)
	}

	exitCode, _, err := execInContainer(ctx, cli, containerID, []string{
		"/bin/sh", "-lc",
		strings.Join([]string{
			"set -eu",
			"SSHD_BAD=0",
			"SSHD_BIN=\"$(command -v sshd 2>/dev/null || true)\"",
			"if [ -z \"$SSHD_BIN\" ]; then SSHD_BIN=\"/usr/sbin/sshd\"; fi",
			"if [ -x \"$SSHD_BIN\" ]; then",
			"  if \"$SSHD_BIN\" -t >/dev/null 2>&1; then exit 0; fi",
			"  SSHD_BAD=1",
			"fi",
			"DROPBEAR_BIN=\"$(command -v dropbear 2>/dev/null || true)\"",
			"if [ -z \"$DROPBEAR_BIN\" ]; then DROPBEAR_BIN=\"/usr/sbin/dropbear\"; fi",
			"if [ -x \"$DROPBEAR_BIN\" ] || [ -x " + fmt.Sprintf("%q", injectedDropbearContainerPath) + " ]; then exit 0; fi",
			"if [ \"$SSHD_BAD\" = \"1\" ]; then exit 43; fi",
			"exit 42",
		}, "\n"),
	})
	if err != nil {
		return ContainerSSHProbeResult{}, fmt.Errorf("probe ssh daemon in container: %w", err)
	}
	if exitCode == sshdMissingExitCode {
		return ContainerSSHProbeResult{Available: false, Reason: "no_sshd"}, nil
	}
	if exitCode == sshDaemonInvalidConfigExitCode {
		return ContainerSSHProbeResult{Available: false, Reason: "invalid_sshd_config"}, nil
	}
	if exitCode != 0 {
		return ContainerSSHProbeResult{Available: false, Reason: "ssh_probe_failed"}, nil
	}

	containerIP := pickContainerIP(insp)
	dialer := net.Dialer{Timeout: 2 * time.Second}
	var firstDialErr error

	if strings.TrimSpace(containerIP) != "" {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(containerIP, "22"))
		if err == nil {
			_ = conn.Close()
			return ContainerSSHProbeResult{
				Available:   true,
				ContainerIP: containerIP,
				Port:        22,
			}, nil
		}
		firstDialErr = err
	}

	publishedHost, publishedPort := pickPublishedSSHHostEndpoint(insp)
	if publishedHost != "" && publishedPort > 0 {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(publishedHost, strconv.Itoa(publishedPort)))
		if err == nil {
			_ = conn.Close()
			return ContainerSSHProbeResult{
				Available:   true,
				ContainerIP: publishedHost,
				Port:        publishedPort,
			}, nil
		}
		if firstDialErr == nil {
			firstDialErr = err
		}
	}

	if strings.TrimSpace(containerIP) == "" && strings.TrimSpace(publishedHost) == "" {
		return ContainerSSHProbeResult{Available: false, Reason: "container_ip_unavailable"}, nil
	}

	return ContainerSSHProbeResult{
		Available:   false,
		Reason:      "ssh_port_unreachable",
		ContainerIP: containerIP,
		Port:        22,
	}, nil
}

func pickContainerIP(insp container.InspectResponse) string {
	if insp.NetworkSettings == nil {
		return ""
	}

	for _, netInfo := range insp.NetworkSettings.Networks {
		if netInfo == nil {
			continue
		}
		if ip := strings.TrimSpace(netInfo.IPAddress); ip != "" {
			return ip
		}
	}

	return strings.TrimSpace(insp.NetworkSettings.IPAddress)
}

func pickPublishedSSHHostEndpoint(insp container.InspectResponse) (string, int) {
	if insp.NetworkSettings == nil {
		return "", 0
	}

	bindings := insp.NetworkSettings.Ports[nat.Port("22/tcp")]
	for _, binding := range bindings {
		hostPort, err := strconv.Atoi(strings.TrimSpace(binding.HostPort))
		if err != nil || hostPort <= 0 || hostPort > 65535 {
			continue
		}

		hostIP := strings.TrimSpace(binding.HostIP)
		switch hostIP {
		case "", "0.0.0.0":
			hostIP = "127.0.0.1"
		case "::":
			hostIP = "::1"
		}

		return hostIP, hostPort
	}

	return "", 0
}
