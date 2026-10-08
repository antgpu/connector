package hw

import (
	"errors"
	"strings"
	"testing"
)

func successfulWorkload() map[string]any {
	return map[string]any{
		"status":     "success",
		"backend":    "cuda-native-streaming-sgemm-v1",
		"score":      790.0,
		"score_unit": "steps_per_sec",
	}
}

func TestParseBenchOutput(t *testing.T) {
	raw := []byte(`
some noisy logs
BENCH_RESULT={"gpu":{"status":"success","per_gpu_tflops":[31.2,29.9],"total_measured_tflops":59.1,"total_spec_tflops":70.0},"network":{"status":"success","download_mbps":920.4,"upload_mbps":870.2},"gpu_workload":{"status":"success","score":1500,"score_unit":"steps_per_sec"}}
`)

	out, err := parseBenchOutput(raw)
	if err != nil {
		t.Fatalf("parseBenchOutput returned error: %v", err)
	}

	if len(out.GPU.PerGPUTFLOPS) != 2 {
		t.Fatalf("expected 2 GPU values, got %d", len(out.GPU.PerGPUTFLOPS))
	}
	if out.GPU.PerGPUTFLOPS[0] != 31.2 || out.GPU.PerGPUTFLOPS[1] != 29.9 {
		t.Fatalf("unexpected per-gpu values: %#v", out.GPU.PerGPUTFLOPS)
	}
	if out.GPU.TotalMeasuredTFLOPS != 59.1 {
		t.Fatalf("unexpected total measured tfops: %f", out.GPU.TotalMeasuredTFLOPS)
	}
	if out.Network.DownloadMbps != 920.4 || out.Network.UploadMbps != 870.2 {
		t.Fatalf("unexpected network values: down=%f up=%f", out.Network.DownloadMbps, out.Network.UploadMbps)
	}
	if got := nestedStatus(out.GPUWorkload); got != "success" {
		t.Fatalf("unexpected workload status: %q", got)
	}
}

func TestParseBenchOutput_GPUWorkload(t *testing.T) {
	raw := []byte(`
BENCH_RESULT={"gpu":{"status":"success"},"network":{"status":"skip"},"gpu_workload":{"status":"success","backend":"cuda-native-streaming-sgemm-v1","score":790,"score_unit":"steps_per_sec","mode":"per_gpu_sequential","config":{"m":512,"n":512,"k":2048}}}
`)

	out, err := parseBenchOutput(raw)
	if err != nil {
		t.Fatalf("parseBenchOutput returned error: %v", err)
	}

	if got := nestedStatus(out.GPUWorkload); got != "success" {
		t.Fatalf("unexpected workload status: %q", got)
	}
	if got := out.GPUWorkload["backend"]; got != "cuda-native-streaming-sgemm-v1" {
		t.Fatalf("unexpected workload backend: %#v", got)
	}
}

func TestParseBenchOutput_MarkerMissing(t *testing.T) {
	_, err := parseBenchOutput([]byte("no marker here"))
	if err == nil {
		t.Fatal("expected error when marker is missing")
	}
}

func TestApplyBenchOutput_SingleGPU(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}
	out.GPU.PerGPUTFLOPS = []float64{42.7}
	out.Network.DownloadMbps = 948.6
	out.Network.UploadMbps = 881.2

	applyBenchOutput(specs, out, BenchRunMode{})

	if len(specs.PerGPUTFLOPS) != 1 || specs.PerGPUTFLOPS[0] != 42.7 {
		t.Fatalf("unexpected per gpu values: %#v", specs.PerGPUTFLOPS)
	}
	if specs.TotalMeasuredTFLOPS != 42.7 {
		t.Fatalf("expected total measured tflops to be single gpu value, got %f", specs.TotalMeasuredTFLOPS)
	}
	if specs.NetDownMbps != 949 || specs.NetUpMbps != 881 {
		t.Fatalf("unexpected rounded network values: down=%d up=%d", specs.NetDownMbps, specs.NetUpMbps)
	}
	if specs.BenchFetchErr != "" {
		t.Fatalf("unexpected bench error: %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_UsesExplicitTotals(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}
	out.GPU.PerGPUTFLOPS = []float64{20.0, 21.0}
	out.GPU.PerGPUSpecTFLOPS = []float64{35.1, 35.1}
	out.GPU.TotalMeasuredTFLOPS = 37.5
	out.GPU.TotalSpecTFLOPS = 70.2

	applyBenchOutput(specs, out, BenchRunMode{})

	if specs.TotalMeasuredTFLOPS != 37.5 {
		t.Fatalf("expected total measured tflops, got %f", specs.TotalMeasuredTFLOPS)
	}
	if specs.TotalSpecTFLOPS != 70.2 {
		t.Fatalf("expected total spec tflops, got %f", specs.TotalSpecTFLOPS)
	}
}

func TestApplyBenchOutput_PreservesGPUWorkload(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}

	applyBenchOutput(specs, out, BenchRunMode{})

	if got := specs.GPUWorkload["backend"]; got != "cuda-native-streaming-sgemm-v1" {
		t.Fatalf("unexpected workload backend: %#v", got)
	}
	if specs.BenchFetchErr != "" {
		t.Fatalf("unexpected bench error for successful workload: %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_ReportsGPUWorkloadError(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: map[string]any{"status": "error", "error": "gpu0=no_valid_samples"}}

	applyBenchOutput(specs, out, BenchRunMode{})

	if !strings.Contains(specs.BenchFetchErr, "gpu_workload=gpu0=no_valid_samples") {
		t.Fatalf("expected workload bench error, got %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_RequiresGPUWorkload(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{}

	applyBenchOutput(specs, out, BenchRunMode{})

	if !strings.Contains(specs.BenchFetchErr, "gpu_workload=skip") {
		t.Fatalf("expected workload missing bench error, got %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_IgnoresNetworkErrorWhenNetworkNotRequested(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}
	out.Network.Status = "skip"
	out.Network.Error = "disabled"

	applyBenchOutput(specs, out, BenchRunMode{RunGPUBase: true})

	if specs.BenchFetchErr != "" {
		t.Fatalf("unexpected bench error: %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_IgnoresSkippedNetworkWhenRequestedButNoIperfHost(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}
	out.Network.Status = "skip"
	out.Network.Error = "BENCH_IPERF_HOST_not_set"

	applyBenchOutput(specs, out, BenchRunMode{RunNetwork: true})

	if specs.BenchFetchErr != "" {
		t.Fatalf("unexpected bench error: %q", specs.BenchFetchErr)
	}
}

func TestApplyBenchOutput_ReportsNetworkErrorWhenNetworkRequested(t *testing.T) {
	specs := &SystemSpecs{}
	out := benchRunnerOutput{GPUWorkload: successfulWorkload()}
	out.Network.Status = "error"
	out.Network.Error = "iperf_failed"

	applyBenchOutput(specs, out, BenchRunMode{RunNetwork: true})

	if !strings.Contains(specs.BenchFetchErr, "network=iperf_failed") {
		t.Fatalf("expected network bench error, got %q", specs.BenchFetchErr)
	}
}

func TestDockerAccessHint_PermissionDenied(t *testing.T) {
	err := errors.New("exit status 1")
	out := []byte("ERROR: permission denied while trying to connect to the docker API at unix:///var/run/docker.sock")

	hint := dockerAccessHint(err, out)
	if hint == "" {
		t.Fatal("expected non-empty hint")
	}
	if want := "permission denied for /var/run/docker.sock"; !strings.Contains(hint, want) {
		t.Fatalf("unexpected hint: %q", hint)
	}
}

func TestDockerAccessHint_DaemonDown(t *testing.T) {
	err := errors.New("exit status 1")
	out := []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")

	hint := dockerAccessHint(err, out)
	if hint != "cannot connect to Docker daemon; ensure Docker service is running" {
		t.Fatalf("unexpected hint: %q", hint)
	}
}

func TestBenchHostSupportedFor_UnsupportedOS(t *testing.T) {
	specs := &SystemSpecs{ComputeAPIs: []string{"CUDA 8.6"}}

	ok, reason := supportsLinuxNVIDIA("darwin", specs)
	if ok {
		t.Fatal("expected unsupported host for darwin")
	}
	if !strings.Contains(reason, "unsupported host os=darwin") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestBenchHostSupportedFor_WindowsHostAccepted(t *testing.T) {
	specs := &SystemSpecs{ComputeAPIs: []string{"CUDA 12.4"}}

	ok, reason := supportsLinuxNVIDIA("windows", specs)
	if !ok {
		t.Fatalf("expected supported windows+nvidia host, reason=%q", reason)
	}
}

func TestBenchHostSupportedFor_MissingNvidiaCapability(t *testing.T) {
	specs := &SystemSpecs{}

	ok, reason := supportsLinuxNVIDIA("linux", specs)
	if ok {
		t.Fatal("expected unsupported host without nvidia capability")
	}
	if reason != "nvidia/cuda capability not detected" {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestBenchHostSupportedFor_CudaAPIAccepted(t *testing.T) {
	specs := &SystemSpecs{ComputeAPIs: []string{"CUDA 12.4"}}

	ok, reason := supportsLinuxNVIDIA("linux", specs)
	if !ok {
		t.Fatalf("expected supported linux+nvidia host, reason=%q", reason)
	}
}

func TestSelectBenchImageVariant_SelectsCudaNative(t *testing.T) {
	specs := &SystemSpecs{ComputeAPIs: []string{"CUDA 12.4"}}
	variant, reason, ok := selectBenchImageVariant("linux", specs)
	if !ok {
		t.Fatalf("expected variant selected, reason=%q", reason)
	}
	if variant.id != "cuda-native" {
		t.Fatalf("unexpected variant id: %q", variant.id)
	}
	if variant.imageTag != cudaNativeBenchImageTag {
		t.Fatalf("unexpected imageTag: %q", variant.imageTag)
	}
	if variant.benchDir != cudaNativeBenchDir {
		t.Fatalf("unexpected benchDir: %q", variant.benchDir)
	}
}

func TestSelectBenchImageVariant_IgnoresVariantEnv(t *testing.T) {
	t.Setenv("ANTGPU_CONNECT_BENCH_VARIANT", "nvidia-linux")
	specs := &SystemSpecs{ComputeAPIs: []string{"CUDA 12.8"}}
	variant, reason, ok := selectBenchImageVariant("linux", specs)
	if !ok {
		t.Fatalf("expected variant selected, reason=%q", reason)
	}
	if variant.id != "cuda-native" {
		t.Fatalf("unexpected variant id: %q", variant.id)
	}
}
