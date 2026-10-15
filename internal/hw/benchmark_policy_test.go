package hw

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCachedBenchmarkDoesNotLaunchDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	specs := &SystemSpecs{}
	EnrichWithBenchmarks(context.Background(), specs, BenchNetworkConfig{}, BenchRunMode{})
	if specs.BenchFetchErr != "" {
		t.Fatalf("cache hit still invoked Docker: %s", specs.BenchFetchErr)
	}
}

func TestNetworkRenewalDoesNotRequestGPU(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture runs on Unix; production entrypoint is platform-independent Docker")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BENCH_TEST_CALLS"
case "$1" in
run) printf '%s\n' '{"end":{"sum_received":{"bits_per_second":100000000}}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("BENCH_TEST_CALLS", calls)
	specs := &SystemSpecs{}
	EnrichWithBenchmarks(context.Background(), specs, BenchNetworkConfig{IperfHost: "192.0.2.1", IperfSeconds: 1}, BenchRunMode{RunNetwork: true})
	if specs.BenchFetchErr != "" || specs.NetDownMbps != 100 || specs.NetUpMbps != 100 {
		t.Fatalf("network-only renewal failed: %+v", specs)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "--gpus") || !strings.Contains(string(raw), "--entrypoint iperf3") || strings.Count(string(raw), "--entrypoint iperf3") != 2 {
		t.Fatalf("network renewal launched CUDA or omitted a direction: %s", raw)
	}
}

func TestNetworkRenewalCannotSilentlySkipMissingServer(t *testing.T) {
	specs := &SystemSpecs{}
	EnrichWithBenchmarks(context.Background(), specs, BenchNetworkConfig{}, BenchRunMode{RunNetwork: true})
	if specs.BenchFetchErr == "" {
		t.Fatal("missing network benchmark target was accepted")
	}
}
