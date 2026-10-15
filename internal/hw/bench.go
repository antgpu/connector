package hw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	cudaNativeBenchImageTag = "docker.io/antgpu/connect-bench-nvidia-cuda@sha256:912a2e34cfd5e976ae7afa70d7ac66640c536d8369a705c1def976fb94a4d96c"
	cudaNativeBenchDir      = "bench/nvidia-cuda"
	benchResultPrefix       = "BENCH_RESULT="
)

type benchRunnerOutput struct {
	GPU struct {
		Status              string         `json:"status"`
		PerGPUTFLOPS        []float64      `json:"per_gpu_tflops"`
		PerGPUSpecTFLOPS    []float64      `json:"per_gpu_tflops_spec"`
		PerGPUVRAMMB        []uint64       `json:"per_gpu_vram_mb"`
		PerGPUMemBWGBPS     []float64      `json:"per_gpu_mem_bw_gbps"`
		TotalSpecTFLOPS     float64        `json:"total_spec_tflops"`
		TotalMeasuredTFLOPS float64        `json:"total_measured_tflops"`
		Compat              map[string]any `json:"compat"`
		ECC                 map[string]any `json:"ecc"`
		Stress              map[string]any `json:"stress"`
		Error               string         `json:"error"`
	} `json:"gpu"`
	Network struct {
		Status       string  `json:"status"`
		DownloadMbps float64 `json:"download_mbps"`
		UploadMbps   float64 `json:"upload_mbps"`
		Error        string  `json:"error"`
	} `json:"network"`
	GPUWorkload map[string]any `json:"gpu_workload"`
	GPUTensor   map[string]any `json:"gpu_tensor"`
}

type BenchNetworkConfig struct {
	IperfHost    string
	IperfPort    int
	IperfSeconds int
}

type BenchRunMode struct {
	RunGPUBase bool
	RunCompat  bool
	RunECC     bool
	RunStress  bool
	RunNetwork bool
}

type benchImageVariant struct {
	id       string
	imageTag string
	benchDir string
	matchFn  func(goos string, specs *SystemSpecs) (bool, string)
}

var benchImageVariants = []benchImageVariant{
	{
		id:       "cuda-native",
		imageTag: cudaNativeBenchImageTag,
		benchDir: cudaNativeBenchDir,
		matchFn:  supportsLinuxNVIDIA,
	},
}

func EnrichWithBenchmarks(ctx context.Context, specs *SystemSpecs, netCfg BenchNetworkConfig, mode BenchRunMode) {
	if specs == nil {
		return
	}

	if !mode.RunGPUBase && !mode.RunCompat && !mode.RunECC && !mode.RunStress && !mode.RunNetwork {
		return
	}
	if mode.RunNetwork && !mode.RunGPUBase && !mode.RunCompat && !mode.RunECC && !mode.RunStress {
		runNetworkBenchmark(ctx, specs, netCfg)
		return
	}

	imageVariant, reason, ok := selectBenchImageVariant(runtime.GOOS, specs)
	if !ok {
		log.Printf("bench: skipped (%s)", reason)
		specs.BenchFetchErr = "bench skipped: " + reason
		return
	}

	output, err := runBenchContainer(ctx, imageVariant.imageTag, imageVariant.benchDir, netCfg, mode)
	if err != nil {
		specs.BenchFetchErr = err.Error()
		return
	}

	applyBenchOutput(specs, output, mode)
}

func HardenBenchRunMode(mode BenchRunMode) BenchRunMode {
	// The authenticated backend plan controls expensive work. ECC is unsupported
	// by the CUDA runner; a skipped ECC object must not renew its validity.
	mode.RunECC = false
	return mode
}

func selectBenchImageVariant(goos string, specs *SystemSpecs) (benchImageVariant, string, bool) {
	lastReason := ""
	for _, variant := range benchImageVariants {
		ok, reason := variant.matchFn(goos, specs)
		if ok {
			return variant, "", true
		}
		if strings.TrimSpace(reason) != "" {
			lastReason = reason
		}
	}
	if lastReason == "" {
		lastReason = "no supported benchmark image for host"
	}
	return benchImageVariant{}, lastReason, false
}

func supportsLinuxNVIDIA(goos string, specs *SystemSpecs) (bool, string) {
	if goos != "linux" && goos != "windows" {
		return false, fmt.Sprintf("unsupported host os=%s (bench container requires linux docker runtime)", goos)
	}
	if !hasNVIDIACapability(specs) {
		return false, "nvidia/cuda capability not detected"
	}
	return true, ""
}

func hasNVIDIACapability(specs *SystemSpecs) bool {
	if specs == nil {
		return false
	}

	for _, api := range specs.ComputeAPIs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(api)), "cuda") {
			return true
		}
	}

	for _, card := range specs.GraphicsCards {
		if card == nil || card.DeviceInfo == nil {
			continue
		}

		if card.DeviceInfo.Vendor != nil {
			vendorID := strings.ToLower(strings.TrimSpace(card.DeviceInfo.Vendor.ID))
			if vendorID == "10de" {
				return true
			}

			vendorName := strings.ToLower(strings.TrimSpace(card.DeviceInfo.Vendor.Name))
			if strings.Contains(vendorName, "nvidia") {
				return true
			}
		}

		if card.DeviceInfo.Product != nil {
			productName := strings.ToLower(strings.TrimSpace(card.DeviceInfo.Product.Name))
			if strings.Contains(productName, "nvidia") {
				return true
			}
		}
	}

	return false
}

func runBenchContainer(ctx context.Context, imageTag string, benchDirHint string, netCfg BenchNetworkConfig, mode BenchRunMode) (benchRunnerOutput, error) {
	log.Printf(
		"bench: start run image=%s bench_dir_hint=%s mode={gpu_base=%t compat=%t ecc=%t stress=%t network=%t} iperf_host_set=%t",
		imageTag,
		benchDirHint,
		mode.RunGPUBase,
		mode.RunCompat,
		mode.RunECC,
		mode.RunStress,
		mode.RunNetwork,
		strings.TrimSpace(netCfg.IperfHost) != "",
	)

	if err := ensureDockerAccess(ctx); err != nil {
		log.Printf("bench: docker access check failed: %v", err)
		return benchRunnerOutput{}, err
	}

	log.Printf("bench: pulling benchmark image=%s", imageTag)
	pullOut, pullErr := runDockerCommandWithLiveOutput(ctx, "pull", imageTag)
	if pullErr != nil {
		pullTail := tailString(pullOut, 2000)
		log.Printf("bench: docker pull failed: %v output=%s", pullErr, pullTail)
		return benchRunnerOutput{}, fmt.Errorf("docker pull bench image: %w: %s", pullErr, pullTail)
	}

	log.Printf("bench: benchmark started; running container image=%s", imageTag)

	baseArgs := []string{
		"run",
		"--rm",
		"--gpus",
		"all",
		"--ipc=host",
		"--ulimit",
		"memlock=-1",
	}
	withStackArgs := append(append([]string{}, baseArgs...),
		"--ulimit",
		"stack=67108864",
	)
	withStackArgs = appendBenchModeEnv(withStackArgs, mode)
	withStackArgs = appendNetBenchEnv(withStackArgs, netCfg)
	withStackArgs = append(withStackArgs, imageTag)

	parsed, runOut, runErr, parseErr := runAndParseBenchOutput(ctx, withStackArgs)
	if runErr == nil && parseErr == nil {
		log.Printf("bench: run completed %s", benchSummary(mode, parsed))
		return parsed, nil
	}

	if runErr != nil {
		log.Printf("bench: run with stack ulimit failed, retrying without stack ulimit: %v output=%s", runErr, tailString(runOut, 1000))
		withoutStackArgs := append(append([]string{}, baseArgs...), appendBenchModeEnv([]string{}, mode)...)
		withoutStackArgs = appendNetBenchEnv(withoutStackArgs, netCfg)
		withoutStackArgs = append(withoutStackArgs, imageTag)

		parsedNoStack, runOutNoStack, runErrNoStack, parseErrNoStack := runAndParseBenchOutput(ctx, withoutStackArgs)
		if runErrNoStack == nil && parseErrNoStack == nil {
			log.Printf("bench: run completed (without stack ulimit) %s", benchSummary(mode, parsedNoStack))
			return parsedNoStack, nil
		}
		if runErrNoStack != nil {
			log.Printf("bench: run failed with and without stack ulimit first_output=%s second_output=%s", tailString(runOut, 1000), tailString(runOutNoStack, 1000))
			return benchRunnerOutput{}, fmt.Errorf("docker run bench (with and without stack ulimit) failed: first=%v (%s); second=%v (%s)", runErr, tailString(runOut, 1000), runErrNoStack, tailString(runOutNoStack, 1000))
		}
		log.Printf("bench: parse failed after retry without stack ulimit: %v", parseErrNoStack)
		return benchRunnerOutput{}, parseErrNoStack
	}

	if parseErr != nil {
		log.Printf("bench: parse failed: %v", parseErr)
		return benchRunnerOutput{}, parseErr
	}

	return parsed, nil
}

func runDockerCommandWithLiveOutput(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "run" {
		name := fmt.Sprintf("antgpu-benchmark-%d-%d", os.Getpid(), time.Now().UnixNano())
		args = append([]string{"run", "--name", name}, args[1:]...)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
		}()
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	var buf bytes.Buffer
	writer := io.MultiWriter(&buf, os.Stdout)
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	return buf.Bytes(), err
}

func appendBenchModeEnv(args []string, mode BenchRunMode) []string {
	enabled := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	args = append(args,
		"-e", "BENCH_RUN_GPU_BASE="+enabled(mode.RunGPUBase),
		"-e", "BENCH_RUN_COMPAT="+enabled(mode.RunCompat),
		"-e", "BENCH_RUN_ECC="+enabled(mode.RunECC),
		"-e", "BENCH_RUN_STRESS="+enabled(mode.RunStress),
		"-e", "BENCH_RUN_NETWORK="+enabled(mode.RunNetwork),
	)
	return args
}

func appendNetBenchEnv(args []string, netCfg BenchNetworkConfig) []string {
	host := strings.TrimSpace(netCfg.IperfHost)
	if host == "" {
		return args
	}
	port := netCfg.IperfPort
	if port <= 0 {
		port = 5201
	}
	seconds := netCfg.IperfSeconds
	if seconds <= 0 {
		seconds = 10
	}
	args = append(args,
		"-e", "BENCH_IPERF_HOST="+host,
		"-e", fmt.Sprintf("BENCH_IPERF_PORT=%d", port),
		"-e", fmt.Sprintf("BENCH_IPERF_SECONDS=%d", seconds),
	)
	return args
}

func runAndParseBenchOutput(ctx context.Context, args []string) (benchRunnerOutput, []byte, error, error) {
	runCmd := exec.CommandContext(ctx, "docker", args...)
	runOut, runErr := runCmd.CombinedOutput()
	parsed, parseErr := parseBenchOutput(runOut)
	return parsed, runOut, runErr, parseErr
}

func applyBenchOutput(specs *SystemSpecs, output benchRunnerOutput, mode BenchRunMode) {
	var measuredSum float64
	if len(output.GPU.PerGPUTFLOPS) > 0 {
		specs.PerGPUTFLOPS = make([]float64, 0, len(output.GPU.PerGPUTFLOPS))
		for _, v := range output.GPU.PerGPUTFLOPS {
			if v <= 0 {
				specs.PerGPUTFLOPS = append(specs.PerGPUTFLOPS, 0)
				continue
			}
			specs.PerGPUTFLOPS = append(specs.PerGPUTFLOPS, v)
			measuredSum += v
		}
	}

	var specSum float64
	if len(output.GPU.PerGPUSpecTFLOPS) > 0 {
		specs.PerGPUSpecTFLOPS = make([]float64, 0, len(output.GPU.PerGPUSpecTFLOPS))
		for _, v := range output.GPU.PerGPUSpecTFLOPS {
			if v <= 0 {
				specs.PerGPUSpecTFLOPS = append(specs.PerGPUSpecTFLOPS, 0)
				continue
			}
			specs.PerGPUSpecTFLOPS = append(specs.PerGPUSpecTFLOPS, v)
			specSum += v
		}
	}

	specs.TotalSpecTFLOPS = output.GPU.TotalSpecTFLOPS
	if specs.TotalSpecTFLOPS <= 0 {
		specs.TotalSpecTFLOPS = specSum
	}
	specs.TotalMeasuredTFLOPS = output.GPU.TotalMeasuredTFLOPS
	if specs.TotalMeasuredTFLOPS <= 0 {
		if len(specs.PerGPUTFLOPS) == 1 && specs.PerGPUTFLOPS[0] > 0 {
			specs.TotalMeasuredTFLOPS = specs.PerGPUTFLOPS[0]
		} else if measuredSum > 0 {
			specs.TotalMeasuredTFLOPS = measuredSum
		}
	}

	if len(output.GPU.PerGPUMemBWGBPS) > 0 {
		specs.PerGPUMemBWGBPS = make([]float64, len(output.GPU.PerGPUMemBWGBPS))
		copy(specs.PerGPUMemBWGBPS, output.GPU.PerGPUMemBWGBPS)
	}
	if len(output.GPU.PerGPUVRAMMB) > 0 {
		specs.GPUVRAMMB = make([]uint64, len(output.GPU.PerGPUVRAMMB))
		copy(specs.GPUVRAMMB, output.GPU.PerGPUVRAMMB)
	}

	if len(output.GPU.ECC) > 0 {
		specs.GPUECC = output.GPU.ECC
	}
	if len(output.GPU.Compat) > 0 {
		specs.GPUCompat = output.GPU.Compat
	}
	if len(output.GPU.Stress) > 0 {
		specs.GPUStress = output.GPU.Stress
	}
	if len(output.GPUWorkload) > 0 {
		specs.GPUWorkload = output.GPUWorkload
	}
	if len(output.GPUTensor) > 0 {
		specs.GPUTensor = output.GPUTensor
	}

	if output.Network.DownloadMbps > 0 {
		specs.NetDownMbps = int(math.Round(output.Network.DownloadMbps))
	}
	if output.Network.UploadMbps > 0 {
		specs.NetUpMbps = int(math.Round(output.Network.UploadMbps))
	}

	errs := make([]string, 0, 2)
	if e := strings.TrimSpace(output.GPU.Error); e != "" {
		errs = append(errs, "gpu="+e)
	}
	if mode.RunNetwork && output.Network.Status != "skip" {
		if e := strings.TrimSpace(output.Network.Error); e != "" {
			errs = append(errs, "network="+e)
		}
	}
	if status := nestedStatus(output.GPUWorkload); mode.RunGPUBase && status != "success" {
		if e, _ := output.GPUWorkload["error"].(string); strings.TrimSpace(e) != "" {
			errs = append(errs, "gpu_workload="+strings.TrimSpace(e))
		} else {
			errs = append(errs, "gpu_workload="+status)
		}
	}
	if len(errs) > 0 {
		specs.BenchFetchErr = strings.Join(errs, "; ")
	}
}

func parseBenchOutput(raw []byte) (benchRunnerOutput, error) {
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, benchResultPrefix) {
			continue
		}

		payload := strings.TrimPrefix(line, benchResultPrefix)
		var out benchRunnerOutput
		if err := json.Unmarshal([]byte(payload), &out); err != nil {
			return benchRunnerOutput{}, fmt.Errorf("decode bench json: %w", err)
		}
		return out, nil
	}

	return benchRunnerOutput{}, fmt.Errorf("bench output marker not found: %s", tailString(raw, 500))
}

func ensureDockerAccess(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "info")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("docker daemon unavailable: %s", dockerAccessHint(err, out))
}

func dockerAccessHint(cmdErr error, out []byte) string {
	msg := strings.ToLower(strings.TrimSpace(string(out)))
	switch {
	case strings.Contains(msg, "permission denied") && strings.Contains(msg, "/var/run/docker.sock"):
		return "permission denied for /var/run/docker.sock; run connect as a user with Docker daemon access (for example add user to docker group)"
	case strings.Contains(msg, "cannot connect to the docker daemon"):
		return "cannot connect to Docker daemon; ensure Docker service is running"
	case strings.Contains(msg, "is the docker daemon running"):
		return "docker daemon is not reachable; ensure Docker service is running"
	default:
		if tail := tailString(out, 500); tail != "" {
			return fmt.Sprintf("%v (%s)", cmdErr, tail)
		}
		return cmdErr.Error()
	}
}

func tailString(raw []byte, limit int) string {
	if limit <= 0 {
		limit = 500
	}
	if len(raw) <= limit {
		return strings.TrimSpace(string(raw))
	}
	return strings.TrimSpace(string(raw[len(raw)-limit:]))
}

func benchSummary(mode BenchRunMode, out benchRunnerOutput) string {
	parts := make([]string, 0, 6)
	if mode.RunGPUBase || mode.RunCompat || mode.RunECC || mode.RunStress {
		parts = append(parts, fmt.Sprintf("gpu_status=%q", out.GPU.Status))
	}
	if mode.RunGPUBase {
		parts = append(parts,
			fmt.Sprintf("total_measured_tflops=%.3f", out.GPU.TotalMeasuredTFLOPS),
			fmt.Sprintf("total_spec_tflops=%.3f", out.GPU.TotalSpecTFLOPS),
		)
	}
	if mode.RunCompat {
		parts = append(parts, fmt.Sprintf("compat_status=%q", nestedStatus(out.GPU.Compat)))
	}
	if mode.RunECC {
		parts = append(parts, fmt.Sprintf("ecc_status=%q", nestedStatus(out.GPU.ECC)))
	}
	if mode.RunStress {
		parts = append(parts, fmt.Sprintf("stress_status=%q", nestedStatus(out.GPU.Stress)))
	}
	if mode.RunNetwork {
		parts = append(parts, fmt.Sprintf("network_status=%q", out.Network.Status))
	}
	if status := nestedStatus(out.GPUWorkload); status != "skip" {
		parts = append(parts, fmt.Sprintf("gpu_workload_status=%q", status))
	}
	if status := nestedStatus(out.GPUTensor); status != "skip" {
		parts = append(parts, fmt.Sprintf("gpu_tensor_status=%q", status))
	}
	if len(parts) == 0 {
		return "no bench groups requested"
	}
	return strings.Join(parts, " ")
}

func nestedStatus(data map[string]any) string {
	if len(data) == 0 {
		return "skip"
	}
	v, ok := data["status"]
	if !ok {
		return "unknown"
	}
	s, ok := v.(string)
	if !ok {
		return "unknown"
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	return s
}
