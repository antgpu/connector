package hw

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A network-only renewal must not launch the image's CUDA entrypoint. The same
// pinned image supplies iperf3 without requesting access to the host GPU.
func runNetworkBenchmark(ctx context.Context, specs *SystemSpecs, cfg BenchNetworkConfig) {
	if strings.TrimSpace(cfg.IperfHost) == "" {
		specs.BenchFetchErr = "network benchmark requires an iperf host"
		return
	}
	port := cfg.IperfPort
	if port <= 0 {
		port = 5201
	}
	seconds := cfg.IperfSeconds
	if seconds <= 0 {
		seconds = 10
	}
	if seconds > 60 {
		specs.BenchFetchErr = "network benchmark duration exceeds 60 seconds"
		return
	}
	for _, download := range []bool{true, false} {
		args := []string{"run", "--rm", "--entrypoint", "iperf3", cudaNativeBenchImageTag, "-c", cfg.IperfHost, "-p", strconv.Itoa(port), "-t", strconv.Itoa(seconds), "-J"}
		if download {
			args = append(args, "-R")
		}
		raw, err := runDockerCommandWithLiveOutput(ctx, args...)
		if err != nil {
			specs.BenchFetchErr = fmt.Sprintf("network benchmark: %v", err)
			return
		}
		var result struct {
			Error string `json:"error"`
			End   struct {
				Received struct {
					Bits float64 `json:"bits_per_second"`
				} `json:"sum_received"`
			} `json:"end"`
		}
		if err = json.Unmarshal(raw, &result); err != nil || result.Error != "" || result.End.Received.Bits <= 0 {
			specs.BenchFetchErr = "network benchmark returned no valid throughput"
			return
		}
		mbps := int(math.Round(result.End.Received.Bits / 1e6))
		if mbps < 1 {
			mbps = 1
		}
		if download {
			specs.NetDownMbps = mbps
		} else {
			specs.NetUpMbps = mbps
		}
	}
}
