package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antgpu/connector/internal/api"
	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/antgpu/connector/internal/hw"
)

// Opt in on a native NVIDIA/Docker host. This does not enroll a machine, contact
// the production API or replace its installed connector. The API fixture supplies
// cached approvals; readiness and bidirectional Docker/iperf traffic are real.
func TestNativeBenchmarkCacheAndNetwork(t *testing.T) {
	if os.Getenv("ANTGPU_TEST_NATIVE_BENCHMARK") != "1" {
		t.Skip("requires a native NVIDIA/Docker test host")
	}
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv(templateScriptsDirEnv, "")
	if err := prepareDockerCLIEnv(runtimeOptions{Foreground: true, StateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var plans atomic.Int32
	var prior api.BenchmarkIdentity
	approved := time.Now().UTC().Add(-time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/machines/benchmarks/plan":
			var input struct {
				Identity api.BenchmarkIdentity `json:"identity"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if plans.Add(1) > 1 && input.Identity != prior {
				t.Error("unchanged host produced an unstable fingerprint")
			}
			prior = input.Identity
			groups := map[string]api.BenchmarkGroup{}
			for _, name := range []string{"gpu_base", "compat", "stress", "network"} {
				groups[name] = api.BenchmarkGroup{Status: "success", Reason: "cached", LastSuccessAt: &approved}
			}
			_ = json.NewEncoder(w).Encode(api.BenchmarkPolicy{Enabled: true, Trusted: true, Ready: true, Groups: groups})
		case "/machines/connector/logs":
			w.WriteHeader(204)
		default:
			t.Errorf("cache hit unexpectedly requested %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	s := api.APIService{APIAddr: server.URL, MachineAuthData: api.MachineAuthData{MachineToken: "native-test-fixture"}, DockerService: internaldocker.NewService(), WorkloadMutex: &sync.RWMutex{}, BenchmarkReady: &atomic.Bool{}}
	for attempt := 0; attempt < 2; attempt++ {
		specs := hw.FetchSpecs()
		if managed, err := runBenchmarkCycle(ctx, &s, specs, true); err != nil || !managed || !s.BenchmarkReady.Load() {
			t.Fatalf("cached native startup: managed=%t ready=%t err=%v", managed, s.BenchmarkReady.Load(), err)
		}
		if specs.TotalMeasuredTFLOPS != 0 || len(specs.GPUWorkload) > 0 || specs.BenchFetchErr != "" {
			t.Fatal("cache hit reran GPU scoring")
		}
	}
	if plans.Load() != 2 {
		t.Fatal("missing cached startup checks")
	}
	// Use the image already validated above, but run only iperf3. Both clients
	// and this disposable server share Docker's default bridge; no host port opens.
	name := "antgpu-benchmark-iperf-test-" + strconv.Itoa(os.Getpid())
	// Read the image from the real readiness run rather than maintaining a
	// second digest pin in this test.
	images, err := exec.CommandContext(ctx, "docker", "images", "--digests", "--format", "{{.Repository}}@{{.Digest}}", "antgpu/connect-bench-nvidia-cuda").Output()
	if err != nil {
		t.Fatal(err)
	}
	var image string
	for _, line := range strings.Fields(string(images)) {
		if strings.Contains(line, "@sha256:") {
			image = line
			break
		}
	}
	if image == "" {
		t.Fatal("pinned benchmark image is missing")
	}
	if raw, err := exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "--name", name, "--entrypoint", "iperf3", image, "-s").CombinedOutput(); err != nil {
		t.Fatalf("start local iperf fixture: %v %s", err, raw)
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	ip, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	result := &hw.SystemSpecs{}
	hw.EnrichWithBenchmarks(ctx, result, hw.BenchNetworkConfig{IperfHost: strings.TrimSpace(string(ip)), IperfPort: 5201, IperfSeconds: 1}, hw.BenchRunMode{RunNetwork: true})
	if result.BenchFetchErr != "" || result.NetDownMbps <= 0 || result.NetUpMbps <= 0 || result.TotalMeasuredTFLOPS != 0 {
		t.Fatalf("native network-only renewal failed: %+v", result)
	}
	t.Logf("two cached GPU readiness checks passed; network-only throughput down=%d up=%d Mbps; no enrollment or GPU scoring", result.NetDownMbps, result.NetUpMbps)
}
