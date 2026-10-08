package docker

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

const workloadNetwork = "antgpu-workloads"
const workloadBridge = "antgpu0"

var workloadNetworkMu sync.Mutex

func configureWorkload(ctx context.Context, cli *client.Client, host *container.HostConfig) error {
	info, err := cli.Info(ctx)
	if err != nil {
		return fmt.Errorf("get workload resource limits: %w", err)
	}
	if info.MemTotal < 512<<20 || info.NCPU < 1 {
		return fmt.Errorf("Docker host capacity is unavailable or insufficient")
	}
	if runtime.GOOS == "linux" && (!info.MemoryLimit || !info.SwapLimit || !info.PidsLimit || !info.CPUCfsQuota || !info.CPUCfsPeriod) {
		return fmt.Errorf("Docker host cannot enforce required memory, swap, PID, and CPU limits")
	}
	reserve := info.MemTotal / 10
	if reserve < 512<<20 {
		reserve = 512 << 20
	}
	memory := info.MemTotal - reserve
	if memory < 256<<20 {
		return fmt.Errorf("insufficient memory after provider reserve")
	}
	cpus := info.NCPU - 1
	if cpus < 1 {
		cpus = 1
	}
	pids := int64(1024)
	host.Resources = container.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: int64(cpus) * 1_000_000_000, PidsLimit: &pids}
	host.CapDrop = []string{"NET_RAW"}
	host.SecurityOpt = []string{"no-new-privileges:true"}
	host.LogConfig = container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "3"}}
	if runtime.GOOS != "linux" {
		return nil
	}
	if err := ensureWorkloadNetwork(ctx, cli); err != nil {
		return err
	}
	host.NetworkMode = container.NetworkMode(workloadNetwork)
	host.Sysctls = map[string]string{"net.ipv6.conf.all.disable_ipv6": "1"}
	return nil
}

func ensureWorkloadNetwork(ctx context.Context, cli *client.Client) error {
	workloadNetworkMu.Lock()
	defer workloadNetworkMu.Unlock()
	// Firewall commands must run on the Docker host, never against a remote
	// daemon while accidentally changing the connector host's firewall.
	if cli.DaemonHost() != "unix:///var/run/docker.sock" {
		return fmt.Errorf("isolated workloads require the local Linux Docker socket /var/run/docker.sock")
	}
	options := map[string]string{"com.docker.network.bridge.name": workloadBridge, "com.docker.network.bridge.enable_icc": "false"}
	existing, err := cli.NetworkInspect(ctx, workloadNetwork, network.InspectOptions{})
	if err == nil {
		if existing.Driver != "bridge" || existing.EnableIPv6 || existing.Labels["antgpu.network"] != "workloads" {
			return fmt.Errorf("refusing unowned or incompatible %s network", workloadNetwork)
		}
		for key, value := range options {
			if existing.Options[key] != value {
				return fmt.Errorf("unsafe workload network option %s", key)
			}
		}
		return installWorkloadFirewall(ctx)
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect workload network: %w", err)
	}
	if err := installWorkloadFirewall(ctx); err != nil {
		return err
	}
	_, err = cli.NetworkCreate(ctx, workloadNetwork, network.CreateOptions{Driver: "bridge", Options: options, Labels: map[string]string{"antgpu.network": "workloads"}})
	if err != nil {
		return fmt.Errorf("create isolated workload network: %w", err)
	}
	return nil
}

func configureWorkloadStorage(ctx context.Context, cli *client.Client, host *container.HostConfig, diskMB int) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	warning, err := CheckStorageOptsSupport(ctx, cli)
	if err != nil {
		return fmt.Errorf("verify workload disk quota support: %w", err)
	}
	if warning != "" {
		return fmt.Errorf("workload disk quotas are required: %s", warning)
	}
	if diskMB <= 0 {
		diskMB = 8 * 1024
	}
	host.StorageOpt["size"] = fmt.Sprintf("%dm", diskMB)
	return nil
}
