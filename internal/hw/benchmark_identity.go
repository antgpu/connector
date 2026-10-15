package hw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

func fingerprint(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ProbeIdentity is deliberately independent of connector build version: an
// ordinary signed connector update does not invalidate an unchanged GPU stack.
func ProbeBenchmarkIdentity(ctx context.Context, specs *SystemSpecs) (gpu, network string, err error) {
	if specs == nil || len(specs.RentableGPUs) == 0 {
		return "", "", fmt.Errorf("no rentable NVIDIA GPU detected")
	}
	var devices []string
	for _, g := range specs.RentableGPUs {
		if strings.TrimSpace(g.UUID) == "" {
			return "", "", fmt.Errorf("GPU identity is unavailable")
		}
		devices = append(devices, fmt.Sprintf("%d/%s/%s/%d/%s", g.RuntimeIndex, g.UUID, g.PCIAddress, g.VRAMMB, g.ComputeAPI))
	}
	sort.Strings(devices)
	driver, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=uuid,driver_version", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return "", "", fmt.Errorf("probe NVIDIA driver: %w", err)
	}
	drivers := strings.Split(strings.TrimSpace(string(driver)), "\n")
	sort.Strings(drivers)
	dockerVersion, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{json .Server}}").Output()
	if err != nil {
		return "", "", fmt.Errorf("probe Docker runtime: %w", err)
	}
	gpu = fingerprint("benchmark-policy-v1", runtime.GOOS, runtime.GOARCH, cudaNativeBenchImageTag, strings.Join(devices, "\n"), strings.Join(drivers, "\n"), strings.TrimSpace(string(dockerVersion)))
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", err
	}
	var addresses []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Ephemeral Docker bridges/veth devices must not invalidate benchmarks.
		if strings.HasPrefix(iface.Name, "veth") || strings.HasPrefix(iface.Name, "br-") || iface.Name == "docker0" {
			continue
		}
		addrs, e := iface.Addrs()
		if e != nil {
			return "", "", e
		}
		for _, a := range addrs {
			addresses = append(addresses, iface.Name+"/"+a.String())
		}
	}
	sort.Strings(addresses)
	network = fingerprint("benchmark-policy-v1", cudaNativeBenchImageTag, strings.Join(addresses, "\n"))
	return gpu, network, nil
}

func QuickBenchmarkReadiness(ctx context.Context) error {
	_, err := runDockerCommandWithLiveOutput(ctx, "run", "--rm", "--gpus", "all", "--entrypoint", "nvidia-smi", cudaNativeBenchImageTag, "-L")
	if err != nil {
		return fmt.Errorf("Docker GPU readiness check: %w", err)
	}
	return nil
}
