//go:build linux

package docker

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/docker/docker/client"
)

func installWorkloadFirewall(ctx context.Context, _ *client.Client) error {
	return configureWorkloadFirewall(ctx, func(ctx context.Context, args ...string) error {
		out, err := exec.CommandContext(ctx, "iptables", append([]string{"-w", "5"}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("iptables %v: %w: %s", args, err, out)
		}
		return nil
	})
}

func validateWorkloadDaemon(cli *client.Client) error {
	if cli.DaemonHost() != "unix:///var/run/docker.sock" {
		return fmt.Errorf("isolated workloads require the local Linux Docker socket /var/run/docker.sock")
	}
	return nil
}
