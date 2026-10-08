//go:build windows

package docker

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// This reviewed, immutable image supplies iptables inside Docker Desktop's Linux
// VM. It never receives the Docker socket, host mounts, host PID namespace, or
// rental configuration. Only this short-lived helper gets NET_ADMIN.
const desktopFirewallImage = "docker:29.5.2-dind@sha256:6b9cd914eb9c6b342c040a49a27a5eb3804453bae6ecc90f7ff96133595a95e8"

func validateWorkloadDaemon(cli *client.Client) error {
	switch strings.ToLower(cli.DaemonHost()) {
	case "npipe:////./pipe/docker_engine", "npipe:////./pipe/dockerdesktoplinuxengine":
		return nil
	default:
		return fmt.Errorf("isolated Windows workloads require the local Docker Desktop Linux engine named pipe")
	}
}

func desktopFirewallHostConfig() *container.HostConfig {
	pids := int64(64)
	return &container.HostConfig{
		NetworkMode: "host", ReadonlyRootfs: true, AutoRemove: true,
		CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Tmpfs:       map[string]string{"/run": "rw,noexec,nosuid,nodev,size=1m"},
		Resources:   container.Resources{Memory: 64 << 20, MemorySwap: 64 << 20, NanoCPUs: 250_000_000, PidsLimit: &pids},
		LogConfig:   container.LogConfig{Type: "none"},
	}
}

func pinnedFirewallImageID(info image.InspectResponse) (string, error) {
	digest := desktopFirewallImage[strings.Index(desktopFirewallImage, "@"):]
	for _, repo := range info.RepoDigests {
		if strings.HasSuffix(repo, digest) && strings.HasPrefix(info.ID, "sha256:") {
			return info.ID, nil
		}
	}
	return "", fmt.Errorf("Docker Desktop firewall image does not match the pinned digest")
}

func desktopHostDestinations() ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("inspect provider addresses: %w", err)
	}
	destinations := append([]string(nil), privateDestinations...)
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			return nil, fmt.Errorf("invalid provider interface address: %w", err)
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			destinations = append(destinations, ipv4.String()+"/32")
		}
	}
	return destinations, nil
}

func installWorkloadFirewall(ctx context.Context, cli *client.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	info, err := cli.Info(ctx)
	if err != nil {
		return err
	}
	if info.OSType != "linux" || !strings.Contains(strings.ToLower(info.OperatingSystem), "docker desktop") {
		return fmt.Errorf("Windows workload isolation requires Docker Desktop's Linux engine")
	}
	destinations, err := desktopHostDestinations()
	if err != nil {
		return err
	}
	inspected, err := cli.ImageInspect(ctx, desktopFirewallImage)
	if errdefs.IsNotFound(err) {
		stream, pullErr := cli.ImagePull(ctx, desktopFirewallImage, image.PullOptions{})
		if pullErr != nil {
			return fmt.Errorf("pull pinned Docker Desktop firewall helper: %w", pullErr)
		}
		err = drainPullProgress(stream, nil)
		stream.Close()
		if err != nil {
			return err
		}
		inspected, err = cli.ImageInspect(ctx, desktopFirewallImage)
	}
	if err != nil {
		return fmt.Errorf("inspect pinned firewall helper: %w", err)
	}
	imageID, err := pinnedFirewallImageID(inspected)
	if err != nil {
		return err
	}
	helper, err := cli.ContainerCreate(ctx, &container.Config{Image: imageID, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"120"}, Labels: map[string]string{"antgpu.helper": "workload-firewall"}}, desktopFirewallHostConfig(), nil, nil, "")
	if err != nil {
		return fmt.Errorf("create Docker Desktop firewall helper: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(cleanup, helper.ID, container.RemoveOptions{Force: true})
	}()
	if err := cli.ContainerStart(ctx, helper.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start Docker Desktop firewall helper: %w", err)
	}
	run := func(ctx context.Context, args ...string) error {
		command := append([]string{"iptables", "-w", "5"}, args...)
		created, err := cli.ContainerExecCreate(ctx, helper.ID, container.ExecOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
		if err != nil {
			return err
		}
		attached, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
		if err != nil {
			return err
		}
		defer attached.Close()
		var output strings.Builder
		if _, err := stdcopy.StdCopy(&output, &output, attached.Reader); err != nil {
			return err
		}
		result, err := cli.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return err
		}
		if result.Running || result.ExitCode != 0 {
			return fmt.Errorf("VM iptables %v failed (exit %d): %s", args, result.ExitCode, output.String())
		}
		return nil
	}
	if err := configureWorkloadFirewallDestinations(ctx, run, destinations); err != nil {
		return fmt.Errorf("install Docker Desktop workload isolation: %w", err)
	}
	return nil
}
