package docker

import (
	"context"
	"fmt"
)

var privateDestinations = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "168.63.129.16/32"}

type firewallRunner func(context.Context, ...string) error

func configureWorkloadFirewall(ctx context.Context, run firewallRunner) error {
	return configureWorkloadFirewallDestinations(ctx, run, privateDestinations)
}

func configureWorkloadFirewallDestinations(ctx context.Context, run firewallRunner, destinations []string) error {
	if err := run(ctx, "-S", "DOCKER-USER"); err != nil {
		return fmt.Errorf("workload isolation requires root and Docker's iptables backend: %w", err)
	}
	for _, chain := range []string{"ANTGPU-EGRESS", "ANTGPU-HOST"} {
		if err := run(ctx, "-S", chain); err != nil {
			if err := run(ctx, "-N", chain); err != nil {
				return err
			}
		}
	}
	ensure := func(chain string, rule ...string) error {
		if run(ctx, append([]string{"-C", chain}, rule...)...) == nil {
			return nil
		}
		return run(ctx, append([]string{"-A", chain}, rule...)...)
	}
	for _, chain := range []string{"ANTGPU-EGRESS", "ANTGPU-HOST"} {
		if err := ensure(chain, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "RETURN"); err != nil {
			return err
		}
	}
	for _, cidr := range destinations {
		if err := ensure("ANTGPU-EGRESS", "-d", cidr, "-j", "REJECT"); err != nil {
			return err
		}
	}
	if err := ensure("ANTGPU-HOST", "-j", "REJECT"); err != nil {
		return err
	}
	// Add hooks only after the policy is complete. RETURN preserves the host's
	// existing firewall decisions for established traffic and public egress.
	for _, hook := range [][2]string{{"DOCKER-USER", "ANTGPU-EGRESS"}, {"INPUT", "ANTGPU-HOST"}} {
		rule := []string{"-i", workloadBridge, "-j", hook[1]}
		if run(ctx, append([]string{"-C", hook[0]}, rule...)...) != nil {
			if err := run(ctx, append([]string{"-I", hook[0], "1"}, rule...)...); err != nil {
				return err
			}
		}
	}
	return nil
}
