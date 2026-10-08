//go:build !linux && !windows

package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/client"
)

func installWorkloadFirewall(context.Context, *client.Client) error {
	return fmt.Errorf("Linux workload firewall is unavailable on this host")
}

func validateWorkloadDaemon(*client.Client) error {
	return fmt.Errorf("workload network isolation is unsupported on this host")
}
