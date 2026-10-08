//go:build !linux

package docker

import (
	"context"
	"fmt"
)

func installWorkloadFirewall(context.Context) error {
	return fmt.Errorf("Linux workload firewall is unavailable on this host")
}
