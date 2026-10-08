package docker

import (
	"context"
	"fmt"
	"log"
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
	if info.OSType != "linux" {
		return fmt.Errorf("workloads require Docker Linux containers")
	}
	if !info.MemoryLimit || !info.SwapLimit || !info.PidsLimit || !info.CPUCfsQuota || !info.CPUCfsPeriod {
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
	if err := ensureWorkloadNetwork(ctx, cli); err != nil {
		return err
	}
	host.NetworkMode = container.NetworkMode(workloadNetwork)
	host.Sysctls = map[string]string{"net.ipv6.conf.all.disable_ipv6": "1", "net.ipv6.conf.default.disable_ipv6": "1"}
	return nil
}

func ensureWorkloadNetwork(ctx context.Context, cli *client.Client) error {
	workloadNetworkMu.Lock()
	defer workloadNetworkMu.Unlock()
	if err := validateWorkloadDaemon(cli); err != nil {
		return err
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
		return installWorkloadFirewall(ctx, cli)
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect workload network: %w", err)
	}
	if err := installWorkloadFirewall(ctx, cli); err != nil {
		return err
	}
	ipv6 := false
	_, err = cli.NetworkCreate(ctx, workloadNetwork, network.CreateOptions{Driver: "bridge", EnableIPv6: &ipv6, Options: options, Labels: map[string]string{"antgpu.network": "workloads"}})
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
		log.Printf("workload storage uses soft enforcement: cannot verify writable-layer quotas: %v", err)
		return nil
	}
	if warning != "" {
		log.Printf("workload storage uses soft enforcement: %s", warning)
		return nil
	}
	if diskMB <= 0 {
		diskMB = 8 * 1024
	}
	if host.StorageOpt == nil {
		host.StorageOpt = make(map[string]string)
	}
	host.StorageOpt["size"] = fmt.Sprintf("%dm", diskMB)
	return nil
}

// Reapply policy at the last start boundary, including after image pulls and
// Docker Desktop restarts. Automatic container restart must not bypass this gate.
func prepareWorkloadStart(ctx context.Context, cli *client.Client, id string) error {
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect workload isolation: %w", err)
	}
	if info.HostConfig == nil || info.HostConfig.NetworkMode != container.NetworkMode(workloadNetwork) || info.HostConfig.Sysctls["net.ipv6.conf.all.disable_ipv6"] != "1" {
		return fmt.Errorf("legacy or incompatible workload networking; recreate this rental before starting")
	}
	if policy := info.HostConfig.RestartPolicy.Name; policy != "" && policy != "no" {
		return fmt.Errorf("automatic workload restart bypasses network isolation checks; recreate this rental")
	}
	return ensureWorkloadNetwork(ctx, cli)
}
