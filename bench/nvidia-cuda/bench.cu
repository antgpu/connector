#include <cuda_runtime.h>
#include <cutlass/cutlass.h>
#include <cutlass/arch/mma.h>
#include <cutlass/epilogue/thread/linear_combination.h>
#include <cutlass/gemm/device/gemm.h>
#include <cutlass/gemm/gemm.h>
#include <cutlass/gemm/threadblock/threadblock_swizzle.h>
#include <cutlass/layout/matrix.h>
#include <cutlass/numeric_types.h>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cctype>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <iomanip>
#include <iostream>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

#define RESULT_PREFIX "BENCH_RESULT="

__global__ void compat_kernel(int *out) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx < 32) {
        out[idx] = idx * 3 + 7;
    }
}

__global__ void fma_kernel(float *out, int iters) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    float x = 1.0f + (float)(idx & 255) * 0.0001f;
    #pragma unroll 4
    for (int i = 0; i < iters; ++i) {
        x = fmaf(x, 1.0000001f, 0.0000001f);
        x = fmaf(x, 0.9999999f, 0.0000002f);
    }
    out[idx] = x;
}

using CutlassSgemm = cutlass::gemm::device::Gemm<
    float, cutlass::layout::RowMajor,
    float, cutlass::layout::RowMajor,
    float, cutlass::layout::RowMajor,
    float>;

using CutlassFp16TensorGemm = cutlass::gemm::device::Gemm<
    cutlass::half_t, cutlass::layout::RowMajor,
    cutlass::half_t, cutlass::layout::RowMajor,
    float, cutlass::layout::RowMajor,
    float,
    cutlass::arch::OpClassTensorOp,
    cutlass::arch::Sm80,
    cutlass::gemm::GemmShape<128, 128, 32>,
    cutlass::gemm::GemmShape<64, 64, 32>,
    cutlass::gemm::GemmShape<16, 8, 16>,
    cutlass::epilogue::thread::LinearCombination<float, 1, float, float>,
    cutlass::gemm::threadblock::GemmIdentityThreadblockSwizzle<>,
    4,
    8,
    8>;

static std::string cutlass_status(cutlass::Status status) {
    switch (status) {
    case cutlass::Status::kSuccess: return "success";
    case cutlass::Status::kErrorMisalignedOperand: return "misaligned_operand";
    case cutlass::Status::kErrorInvalidProblem: return "invalid_problem";
    case cutlass::Status::kErrorNotSupported: return "not_supported";
    case cutlass::Status::kErrorWorkspaceNull: return "workspace_null";
    case cutlass::Status::kErrorInternal: return "internal";
    case cutlass::Status::kErrorArchMismatch: return "arch_mismatch";
    case cutlass::Status::kErrorInsufficientDriver: return "insufficient_driver";
    default: return "status_" + std::to_string((int)status);
    }
}

static std::string json_escape(const std::string &s) {
    std::ostringstream out;
    for (char c : s) {
        switch (c) {
        case '\\': out << "\\\\"; break;
        case '"': out << "\\\""; break;
        case '\n': out << "\\n"; break;
        case '\r': out << "\\r"; break;
        case '\t': out << "\\t"; break;
        default:
            if ((unsigned char)c < 0x20) {
                out << "\\u" << std::hex << std::setw(4) << std::setfill('0') << (int)(unsigned char)c << std::dec;
            } else {
                out << c;
            }
        }
    }
    return out.str();
}

static bool env_bool(const char *name, bool def) {
    const char *raw = std::getenv(name);
    if (!raw || !*raw) return def;
    std::string v(raw);
    std::transform(v.begin(), v.end(), v.begin(), [](unsigned char c){ return (char)std::tolower(c); });
    return v == "1" || v == "true" || v == "yes" || v == "on";
}

static int env_int(const char *name, int def) {
    const char *raw = std::getenv(name);
    if (!raw || !*raw) return def;
    char *end = nullptr;
    long v = std::strtol(raw, &end, 10);
    if (end == raw || v <= 0 || v > 2147483647L) return def;
    return (int)v;
}

static std::string cuda_error(cudaError_t err) {
    if (err == cudaSuccess) return "";
    return std::string(cudaGetErrorName(err)) + ": " + cudaGetErrorString(err);
}

static std::vector<double> sorted_samples(std::vector<double> samples) {
    std::sort(samples.begin(), samples.end());
    return samples;
}

static double median(std::vector<double> samples) {
    if (samples.empty()) return 0.0;
    samples = sorted_samples(samples);
    size_t n = samples.size();
    if (n % 2 == 1) return samples[n / 2];
    return (samples[n / 2 - 1] + samples[n / 2]) / 2.0;
}

static int cores_per_sm(int major, int minor) {
    if (major == 12) return 128;
    if (major == 10) return 128;
    if (major == 9) return 128;
    if (major == 8 && minor == 0) return 64;
    if (major == 8) return 128;
    if (major == 7) return 64;
    if (major == 6 && minor == 0) return 64;
    if (major == 6) return 128;
    if (major == 5) return 128;
    return 64;
}

static double spec_tflops(const cudaDeviceProp &p) {
    if (p.multiProcessorCount <= 0 || p.clockRate <= 0) return 0.0;
    double hz = (double)p.clockRate * 1000.0;
    return (double)p.multiProcessorCount * (double)cores_per_sm(p.major, p.minor) * 2.0 * hz / 1.0e12;
}

static std::string shell_quote(const std::string &s) {
    std::string out = "'";
    for (char c : s) {
        if (c == '\'') out += "'\\''";
        else out += c;
    }
    out += "'";
    return out;
}

static bool safe_host(const std::string &host) {
    if (host.empty() || host.size() > 255) return false;
    for (char c : host) {
        if (!(std::isalnum((unsigned char)c) || c == '.' || c == '-' || c == '_' || c == ':' || c == '[' || c == ']')) return false;
    }
    return true;
}

static std::string run_command(const std::string &cmd) {
    std::string out;
    FILE *pipe = popen((cmd + " 2>&1").c_str(), "r");
    if (!pipe) return "";
    char buf[4096];
    while (fgets(buf, sizeof(buf), pipe)) out += buf;
    pclose(pipe);
    return out;
}

static double parse_max_bits_per_second(const std::string &json) {
    double best = 0.0;
    std::string key = "\"bits_per_second\"";
    size_t pos = 0;
    while ((pos = json.find(key, pos)) != std::string::npos) {
        pos = json.find(':', pos + key.size());
        if (pos == std::string::npos) break;
        ++pos;
        while (pos < json.size() && std::isspace((unsigned char)json[pos])) ++pos;
        char *end = nullptr;
        double v = std::strtod(json.c_str() + pos, &end);
        if (end != json.c_str() + pos && std::isfinite(v) && v > best) best = v;
        pos = (size_t)(end - json.c_str());
    }
    return best;
}

struct NetworkResult {
    std::string status = "skip";
    double down = 0.0;
    double up = 0.0;
    std::string error;
    std::string host;
    int port = 5201;
    int seconds = 10;
};

static std::string join_iperf_errors(const std::vector<std::string> &errors) {
    std::string out;
    for (const auto &e : errors) {
        if (!out.empty()) out += " | ";
        out += e;
    }
    return out;
}

static double run_iperf(const std::string &host, int port, int seconds, bool reverse, std::string &err) {
    constexpr int attempts = 3;
    std::vector<std::string> failures;
    for (int attempt = 1; attempt <= attempts; ++attempt) {
        std::ostringstream cmd;
        cmd << "iperf3 --json --client " << shell_quote(host)
            << " --port " << port
            << " --time " << seconds
            << " --connect-timeout 5000";
        if (reverse) cmd << " --reverse";
        std::string out = run_command(cmd.str());
        double bps = parse_max_bits_per_second(out);
        if (bps > 0) {
            err.clear();
            return bps / 1.0e6;
        }
        std::string failure = out.empty() ? "iperf_failed" : out.substr(0, 220);
        failures.push_back("attempt " + std::to_string(attempt) + "/" + std::to_string(attempts) + ": " + failure);
        if (attempt < attempts) std::this_thread::sleep_for(std::chrono::seconds(2));
    }
    err = join_iperf_errors(failures);
    return 0.0;
}

static NetworkResult run_network(bool enabled) {
    NetworkResult r;
    r.port = env_int("BENCH_IPERF_PORT", 5201);
    r.seconds = env_int("BENCH_IPERF_SECONDS", 10);
    const char *host_raw = std::getenv("BENCH_IPERF_HOST");
    r.host = host_raw ? std::string(host_raw) : "";
    if (!enabled) {
        r.status = "skip";
        r.error = "disabled";
        return r;
    }
    if (!safe_host(r.host)) {
        r.status = "skip";
        r.error = "BENCH_IPERF_HOST_not_set";
        return r;
    }
    std::string down_err, up_err;
    r.down = run_iperf(r.host, r.port, r.seconds, true, down_err);
    r.up = run_iperf(r.host, r.port, r.seconds, false, up_err);
    if (r.down > 0 && r.up > 0) r.status = "success";
    else if (r.down > 0 || r.up > 0) r.status = "partial";
    else r.status = "error";
    if (!down_err.empty()) r.error += "down=" + down_err;
    if (!up_err.empty()) {
        if (!r.error.empty()) r.error += "; ";
        r.error += "up=" + up_err;
    }
    return r;
}

struct DeviceResult {
    int index = 0;
    std::string name;
    int major = 0;
    int minor = 0;
    unsigned long long vram_mb = 0;
    int sm_count = 0;
    int clock_khz = 0;
    double spec = 0.0;
    double measured = 0.0;
    double mem_bw = 0.0;
    std::string compat = "skip";
    std::string stress = "skip";
    std::vector<std::string> errors;
};

static void add_error(DeviceResult &r, const std::string &prefix, cudaError_t err) {
    if (err != cudaSuccess) r.errors.push_back(prefix + ": " + cuda_error(err));
}

static void run_compat(DeviceResult &r) {
    cudaSetDevice(r.index);
    int host[32] = {0};
    int *dev = nullptr;
    cudaError_t err = cudaMalloc(&dev, sizeof(host));
    if (err != cudaSuccess) {
        add_error(r, "compat_alloc", err);
        r.compat = "error";
        return;
    }
    compat_kernel<<<1, 32>>>(dev);
    err = cudaGetLastError();
    if (err == cudaSuccess) err = cudaMemcpy(host, dev, sizeof(host), cudaMemcpyDeviceToHost);
    if (err == cudaSuccess) err = cudaDeviceSynchronize();
    cudaFree(dev);
    if (err != cudaSuccess) {
        add_error(r, "compat_kernel", err);
        r.compat = "error";
        return;
    }
    for (int i = 0; i < 32; ++i) {
        if (host[i] != i * 3 + 7) {
            r.errors.push_back("compat_mismatch");
            r.compat = "error";
            return;
        }
    }
    r.compat = "success";
}

static double run_compute(DeviceResult &r) {
    cudaSetDevice(r.index);
    const int threads = 256;
    const int blocks = std::max(1, r.sm_count * env_int("BENCH_CUDA_BLOCKS_PER_SM", 64));
    const int iters = env_int("BENCH_CUDA_FMA_ITERS", 65536);
    float *out = nullptr;
    cudaError_t err = cudaMalloc(&out, (size_t)blocks * threads * sizeof(float));
    if (err != cudaSuccess) {
        add_error(r, "compute_alloc", err);
        return 0.0;
    }
    for (int i = 0; i < 2; ++i) fma_kernel<<<blocks, threads>>>(out, iters / 8);
    err = cudaDeviceSynchronize();
    if (err != cudaSuccess) {
        add_error(r, "compute_warmup", err);
        cudaFree(out);
        return 0.0;
    }
    std::vector<double> samples;
    for (int sample = 0; sample < 5; ++sample) {
        cudaEvent_t start, stop;
        cudaEventCreate(&start);
        cudaEventCreate(&stop);
        cudaEventRecord(start);
        fma_kernel<<<blocks, threads>>>(out, iters);
        cudaEventRecord(stop);
        err = cudaEventSynchronize(stop);
        float ms = 0.0f;
        if (err == cudaSuccess) err = cudaEventElapsedTime(&ms, start, stop);
        cudaEventDestroy(start);
        cudaEventDestroy(stop);
        if (err != cudaSuccess || ms <= 0.0f) {
            if (err != cudaSuccess) add_error(r, "compute_kernel", err);
            continue;
        }
        double flops = (double)blocks * (double)threads * (double)iters * 4.0;
        double tflops = flops / ((double)ms / 1000.0) / 1.0e12;
        if (std::isfinite(tflops) && tflops > 0.0 && tflops < 100000.0) samples.push_back(tflops);
    }
    cudaFree(out);
    return median(samples);
}

static double run_mem_bw(DeviceResult &r) {
    cudaSetDevice(r.index);
    size_t bytes = 256ULL * 1024ULL * 1024ULL;
    if (r.vram_mb > 0) {
        size_t cap = (size_t)r.vram_mb * 1024ULL * 1024ULL / 16ULL;
        bytes = std::max<size_t>(64ULL * 1024ULL * 1024ULL, std::min(bytes, cap));
    }
    char *src = nullptr, *dst = nullptr;
    cudaError_t err = cudaMalloc(&src, bytes);
    if (err == cudaSuccess) err = cudaMalloc(&dst, bytes);
    if (err != cudaSuccess) {
        add_error(r, "mem_alloc", err);
        if (src) cudaFree(src);
        if (dst) cudaFree(dst);
        return 0.0;
    }
    cudaMemset(src, 1, bytes);
    std::vector<double> samples;
    for (int sample = 0; sample < 5; ++sample) {
        cudaEvent_t start, stop;
        cudaEventCreate(&start);
        cudaEventCreate(&stop);
        cudaEventRecord(start);
        for (int i = 0; i < 64; ++i) cudaMemcpyAsync(dst, src, bytes, cudaMemcpyDeviceToDevice);
        cudaEventRecord(stop);
        err = cudaEventSynchronize(stop);
        float ms = 0.0f;
        if (err == cudaSuccess) err = cudaEventElapsedTime(&ms, start, stop);
        cudaEventDestroy(start);
        cudaEventDestroy(stop);
        if (err == cudaSuccess && ms > 0.0f) {
            double gbps = (double)bytes * 64.0 / ((double)ms / 1000.0) / 1.0e9;
            if (std::isfinite(gbps) && gbps > 0.0) samples.push_back(gbps);
        }
    }
    cudaFree(src);
    cudaFree(dst);
    return median(samples);
}

static void run_stress(DeviceResult &r, int seconds) {
    cudaSetDevice(r.index);
    const int threads = 256;
    const int blocks = std::max(1, r.sm_count * 32);
    float *out = nullptr;
    cudaError_t err = cudaMalloc(&out, (size_t)blocks * threads * sizeof(float));
    if (err != cudaSuccess) {
        add_error(r, "stress_alloc", err);
        r.stress = "error";
        return;
    }
    auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(seconds);
    int launches = 0;
    while (std::chrono::steady_clock::now() < deadline) {
        fma_kernel<<<blocks, threads>>>(out, 8192);
        err = cudaDeviceSynchronize();
        if (err != cudaSuccess) {
            add_error(r, "stress_kernel", err);
            cudaFree(out);
            r.stress = "error";
            return;
        }
        ++launches;
    }
    cudaFree(out);
    r.stress = launches > 0 ? "success" : "error";
    if (launches <= 0) r.errors.push_back("stress_no_launches");
}

static DeviceResult run_device(int index, bool run_base, bool run_compat_flag, bool run_stress_flag) {
    DeviceResult r;
    r.index = index;
    cudaSetDevice(index);
    cudaDeviceProp p{};
    cudaError_t err = cudaGetDeviceProperties(&p, index);
    if (err != cudaSuccess) {
        add_error(r, "device_properties", err);
        return r;
    }
    r.name = p.name;
    r.major = p.major;
    r.minor = p.minor;
    r.vram_mb = (unsigned long long)(p.totalGlobalMem / (1024ULL * 1024ULL));
    r.sm_count = p.multiProcessorCount;
    r.clock_khz = p.clockRate;
    r.spec = spec_tflops(p);
    if (run_compat_flag) run_compat(r);
    else r.compat = "skip";
    if (run_base) {
        r.measured = run_compute(r);
        r.mem_bw = run_mem_bw(r);
        if (r.measured <= 0.0) r.errors.push_back("compute_no_result");
    }
    if (run_stress_flag) run_stress(r, env_int("BENCH_STRESS_SECONDS", 60));
    else r.stress = "skip";
    return r;
}

static std::string join_errors(const std::vector<std::string> &errs) {
    std::ostringstream out;
    for (size_t i = 0; i < errs.size(); ++i) {
        if (i) out << "; ";
        out << errs[i];
    }
    return out.str();
}

struct GPUWorkloadConfig {
    int m = 512;
    int n = 512;
    int k = 2048;
    int steps = 20;
    int samples = 5;
    int warmup_steps = 3;
};

struct GPUWorkloadDeviceResult {
    int index = 0;
    std::string status = "skip";
    double steps_per_sec = 0.0;
    double median_ms_per_step = 0.0;
    double effective_tflops = 0.0;
    double h2d_gbps_effective = 0.0;
    double d2h_gbps_effective = 0.0;
    std::vector<double> samples;
    std::string error;
};

struct GPUWorkloadResult {
    std::string status = "error";
    std::vector<GPUWorkloadDeviceResult> per_gpu;
    double score = 0.0;
    double min_steps_per_sec = 0.0;
    double max_steps_per_sec = 0.0;
    double imbalance_pct = 0.0;
    std::string error;
};

struct GPUTensorConfig {
    int m = 4096;
    int n = 4096;
    int k = 4096;
    int steps = 20;
    int samples = 5;
    int warmup_steps = 3;
};

struct GPUTensorDeviceResult {
    int index = 0;
    std::string status = "skip";
    double measured_tflops = 0.0;
    double median_ms_per_step = 0.0;
    std::vector<double> samples;
    std::string error;
};

struct GPUTensorResult {
    std::string status = "error";
    std::string backend = "cutlass-fp16-tensor-gemm-v1";
    std::string precision = "fp16_tensor";
    std::vector<GPUTensorDeviceResult> per_gpu;
    double total_measured_tflops = 0.0;
    std::string error;
};

static GPUWorkloadConfig gpu_workload_config() {
    return GPUWorkloadConfig{};
}

static GPUTensorConfig gpu_tensor_config() {
    return GPUTensorConfig{};
}

static unsigned long long workload_h2d_bytes_per_step(const GPUWorkloadConfig &cfg) {
    return (unsigned long long)cfg.m * (unsigned long long)cfg.k * sizeof(float)
        + (unsigned long long)cfg.k * (unsigned long long)cfg.n * sizeof(float);
}

static unsigned long long workload_d2h_bytes_per_step(const GPUWorkloadConfig&) {
    return sizeof(float);
}

static double workload_flops_per_step(const GPUWorkloadConfig &cfg) {
    return 2.0 * (double)cfg.m * (double)cfg.n * (double)cfg.k;
}

static void fill_input(float *data, size_t count, int seed) {
    for (size_t i = 0; i < count; ++i) {
        int v = (int)((i + (size_t)seed * 17U) % 251U) - 125;
        data[i] = (float)v * 0.001f;
    }
}

static std::string run_workload_step(const GPUWorkloadConfig &cfg, const float *h_a, const float *h_b, float *h_probe, float *d_a, float *d_b, float *d_c, CutlassSgemm &gemm_op) {
    size_t a_bytes = (size_t)cfg.m * (size_t)cfg.k * sizeof(float);
    size_t b_bytes = (size_t)cfg.k * (size_t)cfg.n * sizeof(float);
    cudaError_t err = cudaMemcpyAsync(d_a, h_a, a_bytes, cudaMemcpyHostToDevice);
    if (err != cudaSuccess) return "h2d_a: " + cuda_error(err);
    err = cudaMemcpyAsync(d_b, h_b, b_bytes, cudaMemcpyHostToDevice);
    if (err != cudaSuccess) return "h2d_b: " + cuda_error(err);
    cutlass::Status status = gemm_op();
    if (status != cutlass::Status::kSuccess) return "cutlass_gemm: " + cutlass_status(status);
    err = cudaGetLastError();
    if (err != cudaSuccess) return "cutlass_launch: " + cuda_error(err);
    err = cudaMemcpyAsync(h_probe, d_c, sizeof(float), cudaMemcpyDeviceToHost);
    if (err != cudaSuccess) return "d2h_probe: " + cuda_error(err);
    return "";
}

static GPUWorkloadDeviceResult run_gpu_workload_device(int index, const GPUWorkloadConfig &cfg) {
    GPUWorkloadDeviceResult r;
    r.index = index;
    r.status = "error";
    cudaSetDevice(index);

    size_t a_count = (size_t)cfg.m * (size_t)cfg.k;
    size_t b_count = (size_t)cfg.k * (size_t)cfg.n;
    size_t c_count = (size_t)cfg.m * (size_t)cfg.n;
    size_t a_bytes = a_count * sizeof(float);
    size_t b_bytes = b_count * sizeof(float);
    size_t c_bytes = c_count * sizeof(float);

    float *h_a = nullptr, *h_b = nullptr, *h_probe = nullptr;
    float *d_a = nullptr, *d_b = nullptr, *d_c = nullptr;
    cudaError_t err = cudaHostAlloc(&h_a, a_bytes, cudaHostAllocDefault);
    if (err == cudaSuccess) err = cudaHostAlloc(&h_b, b_bytes, cudaHostAllocDefault);
    if (err == cudaSuccess) err = cudaHostAlloc(&h_probe, sizeof(float), cudaHostAllocDefault);
    if (err == cudaSuccess) err = cudaMalloc(&d_a, a_bytes);
    if (err == cudaSuccess) err = cudaMalloc(&d_b, b_bytes);
    if (err == cudaSuccess) err = cudaMalloc(&d_c, c_bytes);
    if (err != cudaSuccess) {
        r.error = "alloc: " + cuda_error(err);
        if (h_a) cudaFreeHost(h_a);
        if (h_b) cudaFreeHost(h_b);
        if (h_probe) cudaFreeHost(h_probe);
        if (d_a) cudaFree(d_a);
        if (d_b) cudaFree(d_b);
        if (d_c) cudaFree(d_c);
        return r;
    }

    fill_input(h_a, a_count, index + 1);
    fill_input(h_b, b_count, index + 11);
    *h_probe = 0.0f;

    CutlassSgemm gemm_op;
    CutlassSgemm::Arguments gemm_args(
        cutlass::gemm::GemmCoord(cfg.m, cfg.n, cfg.k),
        {d_a, cfg.k},
        {d_b, cfg.n},
        {d_c, cfg.n},
        {d_c, cfg.n},
        {1.0f, 0.0f});
    cutlass::Status status = gemm_op.can_implement(gemm_args);
    if (status == cutlass::Status::kSuccess) status = gemm_op.initialize(gemm_args);
    if (status != cutlass::Status::kSuccess) {
        r.error = "cutlass_init: " + cutlass_status(status);
        cudaFreeHost(h_a); cudaFreeHost(h_b); cudaFreeHost(h_probe);
        cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
        return r;
    }

    for (int i = 0; i < cfg.warmup_steps; ++i) {
        std::string step_error = run_workload_step(cfg, h_a, h_b, h_probe, d_a, d_b, d_c, gemm_op);
        if (step_error.empty()) err = cudaDeviceSynchronize();
        if (!step_error.empty() || err != cudaSuccess) {
            r.error = step_error.empty() ? ("warmup_sync: " + cuda_error(err)) : ("warmup: " + step_error);
            cudaFreeHost(h_a); cudaFreeHost(h_b); cudaFreeHost(h_probe);
            cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
            return r;
        }
    }

    double flops = workload_flops_per_step(cfg);
    double h2d_bytes = (double)workload_h2d_bytes_per_step(cfg);
    double d2h_bytes = (double)workload_d2h_bytes_per_step(cfg);
    std::vector<double> tflop_samples;
    std::vector<double> h2d_samples;
    std::vector<double> d2h_samples;
    std::vector<double> ms_per_step_samples;

    for (int sample = 0; sample < cfg.samples; ++sample) {
        cudaEvent_t start, stop;
        cudaEventCreate(&start);
        cudaEventCreate(&stop);
        cudaEventRecord(start);
        std::string step_error;
        for (int step = 0; step < cfg.steps; ++step) {
            step_error = run_workload_step(cfg, h_a, h_b, h_probe, d_a, d_b, d_c, gemm_op);
            if (!step_error.empty()) break;
        }
        if (step_error.empty()) cudaEventRecord(stop);
        if (step_error.empty()) err = cudaEventSynchronize(stop);
        float ms = 0.0f;
        if (step_error.empty() && err == cudaSuccess) err = cudaEventElapsedTime(&ms, start, stop);
        cudaEventDestroy(start);
        cudaEventDestroy(stop);
        if (!step_error.empty() || err != cudaSuccess) {
            r.error = step_error.empty() ? ("sample: " + cuda_error(err)) : ("sample: " + step_error);
            continue;
        }
        if (ms <= 0.0f) continue;
        double seconds = (double)ms / 1000.0;
        double steps_per_sec = (double)cfg.steps / seconds;
        if (std::isfinite(steps_per_sec) && steps_per_sec > 0.0) {
            r.samples.push_back(steps_per_sec);
            ms_per_step_samples.push_back((double)ms / (double)cfg.steps);
            tflop_samples.push_back(flops * (double)cfg.steps / seconds / 1.0e12);
            h2d_samples.push_back(h2d_bytes * (double)cfg.steps / seconds / 1.0e9);
            d2h_samples.push_back(d2h_bytes * (double)cfg.steps / seconds / 1.0e9);
        }
    }

    cudaFreeHost(h_a); cudaFreeHost(h_b); cudaFreeHost(h_probe);
    cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);

    r.steps_per_sec = median(r.samples);
    r.median_ms_per_step = median(ms_per_step_samples);
    r.effective_tflops = median(tflop_samples);
    r.h2d_gbps_effective = median(h2d_samples);
    r.d2h_gbps_effective = median(d2h_samples);
    if (r.steps_per_sec > 0.0) {
        r.status = "success";
        r.error.clear();
    } else if (r.error.empty()) {
        r.error = "no_valid_samples";
    }
    return r;
}

static GPUWorkloadResult run_gpu_workload(const std::vector<DeviceResult> &devices, const GPUWorkloadConfig &cfg, const std::string &fatal) {
    GPUWorkloadResult r;
    if (!fatal.empty() || devices.empty()) {
        r.error = fatal.empty() ? "no_cuda_devices" : fatal;
        return r;
    }

    bool any_error = false;
    for (const auto &d : devices) {
        GPUWorkloadDeviceResult dev = run_gpu_workload_device(d.index, cfg);
        if (dev.status != "success") {
            any_error = true;
            if (!r.error.empty()) r.error += "; ";
            r.error += "gpu" + std::to_string(d.index) + "=" + dev.error;
        }
        if (dev.steps_per_sec > 0.0) {
            r.score += dev.steps_per_sec;
            if (r.min_steps_per_sec <= 0.0 || dev.steps_per_sec < r.min_steps_per_sec) r.min_steps_per_sec = dev.steps_per_sec;
            if (dev.steps_per_sec > r.max_steps_per_sec) r.max_steps_per_sec = dev.steps_per_sec;
        }
        r.per_gpu.push_back(dev);
    }

    if (r.max_steps_per_sec > 0.0 && r.min_steps_per_sec > 0.0) {
        r.imbalance_pct = (r.max_steps_per_sec - r.min_steps_per_sec) / r.max_steps_per_sec * 100.0;
    }
    r.status = any_error ? "error" : "success";
    return r;
}

static double tensor_flops_per_step(const GPUTensorConfig &cfg) {
    return 2.0 * (double)cfg.m * (double)cfg.n * (double)cfg.k;
}

static GPUTensorDeviceResult run_gpu_tensor_device(int index, const GPUTensorConfig &cfg) {
    GPUTensorDeviceResult r;
    r.index = index;
    r.status = "error";
    cudaSetDevice(index);

    size_t a_count = (size_t)cfg.m * (size_t)cfg.k;
    size_t b_count = (size_t)cfg.k * (size_t)cfg.n;
    size_t c_count = (size_t)cfg.m * (size_t)cfg.n;
    size_t a_bytes = a_count * sizeof(cutlass::half_t);
    size_t b_bytes = b_count * sizeof(cutlass::half_t);
    size_t c_bytes = c_count * sizeof(float);

    cutlass::half_t *d_a = nullptr, *d_b = nullptr;
    float *d_c = nullptr;
    cudaError_t err = cudaMalloc(&d_a, a_bytes);
    if (err == cudaSuccess) err = cudaMalloc(&d_b, b_bytes);
    if (err == cudaSuccess) err = cudaMalloc(&d_c, c_bytes);
    if (err != cudaSuccess) {
        r.error = "alloc: " + cuda_error(err);
        if (d_a) cudaFree(d_a);
        if (d_b) cudaFree(d_b);
        if (d_c) cudaFree(d_c);
        return r;
    }
    cudaMemset(d_a, 1, a_bytes);
    cudaMemset(d_b, 2, b_bytes);
    cudaMemset(d_c, 0, c_bytes);

    CutlassFp16TensorGemm gemm_op;
    CutlassFp16TensorGemm::Arguments gemm_args(
        cutlass::gemm::GemmCoord(cfg.m, cfg.n, cfg.k),
        {d_a, cfg.k},
        {d_b, cfg.n},
        {d_c, cfg.n},
        {d_c, cfg.n},
        {1.0f, 0.0f});
    cutlass::Status status = gemm_op.can_implement(gemm_args);
    if (status == cutlass::Status::kSuccess) status = gemm_op.initialize(gemm_args);
    if (status != cutlass::Status::kSuccess) {
        r.error = "cutlass_init: " + cutlass_status(status);
        cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
        return r;
    }

    for (int i = 0; i < cfg.warmup_steps; ++i) {
        status = gemm_op();
        if (status != cutlass::Status::kSuccess) {
            r.error = "warmup_gemm: " + cutlass_status(status);
            cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
            return r;
        }
    }
    err = cudaDeviceSynchronize();
    if (err != cudaSuccess) {
        r.error = "warmup_sync: " + cuda_error(err);
        cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);
        return r;
    }

    double flops = tensor_flops_per_step(cfg);
    std::vector<double> ms_per_step_samples;
    for (int sample = 0; sample < cfg.samples; ++sample) {
        cudaEvent_t start, stop;
        cudaEventCreate(&start);
        cudaEventCreate(&stop);
        cudaEventRecord(start);
        status = cutlass::Status::kSuccess;
        for (int step = 0; step < cfg.steps; ++step) {
            status = gemm_op();
            if (status != cutlass::Status::kSuccess) break;
        }
        if (status == cutlass::Status::kSuccess) cudaEventRecord(stop);
        if (status == cutlass::Status::kSuccess) err = cudaEventSynchronize(stop);
        float ms = 0.0f;
        if (status == cutlass::Status::kSuccess && err == cudaSuccess) err = cudaEventElapsedTime(&ms, start, stop);
        cudaEventDestroy(start);
        cudaEventDestroy(stop);
        if (status != cutlass::Status::kSuccess || err != cudaSuccess) {
            r.error = status == cutlass::Status::kSuccess ? ("sample: " + cuda_error(err)) : ("sample_gemm: " + cutlass_status(status));
            continue;
        }
        if (ms <= 0.0f) continue;
        double seconds = (double)ms / 1000.0;
        double tflops = flops * (double)cfg.steps / seconds / 1.0e12;
        if (std::isfinite(tflops) && tflops > 0.0 && tflops < 1000000.0) {
            r.samples.push_back(tflops);
            ms_per_step_samples.push_back((double)ms / (double)cfg.steps);
        }
    }

    cudaFree(d_a); cudaFree(d_b); cudaFree(d_c);

    r.measured_tflops = median(r.samples);
    r.median_ms_per_step = median(ms_per_step_samples);
    if (r.measured_tflops > 0.0) {
        r.status = "success";
        r.error.clear();
    } else if (r.error.empty()) {
        r.error = "no_valid_samples";
    }
    return r;
}

static GPUTensorResult run_gpu_tensor(const std::vector<DeviceResult> &devices, const GPUTensorConfig &cfg, const std::string &fatal) {
    GPUTensorResult r;
    if (!fatal.empty() || devices.empty()) {
        r.error = fatal.empty() ? "no_cuda_devices" : fatal;
        return r;
    }

    bool any_error = false;
    for (const auto &d : devices) {
        GPUTensorDeviceResult dev = run_gpu_tensor_device(d.index, cfg);
        if (dev.status != "success") {
            any_error = true;
            if (!r.error.empty()) r.error += "; ";
            r.error += "gpu" + std::to_string(d.index) + "=" + dev.error;
        }
        if (dev.measured_tflops > 0.0) r.total_measured_tflops += dev.measured_tflops;
        r.per_gpu.push_back(dev);
    }

    r.status = any_error ? "partial" : "success";
    return r;
}

static void print_gpu_tensor(std::ostringstream &out, const GPUTensorConfig &cfg, const GPUTensorResult &r) {
    out << "\"gpu_tensor\":{";
    out << "\"status\":\"" << r.status << "\",";
    out << "\"backend\":\"" << r.backend << "\",";
    out << "\"precision\":\"" << r.precision << "\",";
    out << "\"total_measured_tflops\":" << r.total_measured_tflops << ",";
    out << "\"per_gpu\":[";
    for (size_t i = 0; i < r.per_gpu.size(); ++i) {
        const auto &d = r.per_gpu[i];
        if (i) out << ',';
        out << "{\"index\":" << d.index
            << ",\"status\":\"" << d.status << "\""
            << ",\"measured_tflops\":" << d.measured_tflops
            << ",\"median_ms_per_step\":" << d.median_ms_per_step
            << ",\"samples\":[";
        for (size_t j = 0; j < d.samples.size(); ++j) { if (j) out << ','; out << d.samples[j]; }
        out << "],\"error\":\"" << json_escape(d.error) << "\"}";
    }
    out << "],";
    out << "\"config\":{";
    out << "\"m\":" << cfg.m << ",";
    out << "\"n\":" << cfg.n << ",";
    out << "\"k\":" << cfg.k << ",";
    out << "\"steps\":" << cfg.steps << ",";
    out << "\"samples\":" << cfg.samples << ",";
    out << "\"warmup_steps\":" << cfg.warmup_steps << "},";
    out << "\"error\":\"" << json_escape(r.error) << "\"}";
}

static void print_gpu_workload(std::ostringstream &out, const GPUWorkloadConfig &cfg, const GPUWorkloadResult &r) {
    unsigned long long h2d_bytes = workload_h2d_bytes_per_step(cfg);
    unsigned long long d2h_bytes = workload_d2h_bytes_per_step(cfg);
    double flops = workload_flops_per_step(cfg);

    out << "\"gpu_workload\":{";
    out << "\"status\":\"" << r.status << "\",";
    out << "\"backend\":\"cuda-native-streaming-cutlass-sgemm-v1\",";
    out << "\"score\":" << r.score << ",";
    out << "\"score_unit\":\"steps_per_sec\",";
    out << "\"mode\":\"per_gpu_sequential\",";
    out << "\"per_gpu\":[";
    for (size_t i = 0; i < r.per_gpu.size(); ++i) {
        const auto &d = r.per_gpu[i];
        if (i) out << ',';
        out << "{\"index\":" << d.index
            << ",\"status\":\"" << d.status << "\""
            << ",\"score\":" << d.steps_per_sec
            << ",\"steps_per_sec\":" << d.steps_per_sec
            << ",\"median_ms_per_step\":" << d.median_ms_per_step
            << ",\"effective_tflops\":" << d.effective_tflops
            << ",\"h2d_gbps_effective\":" << d.h2d_gbps_effective
            << ",\"d2h_gbps_effective\":" << std::setprecision(6) << d.d2h_gbps_effective << std::setprecision(3)
            << ",\"samples\":[";
        for (size_t j = 0; j < d.samples.size(); ++j) { if (j) out << ','; out << d.samples[j]; }
        out << "],\"error\":\"" << json_escape(d.error) << "\"}";
    }
    out << "],";
    out << "\"total_steps_per_sec_sum\":" << r.score << ",";
    out << "\"min_steps_per_sec\":" << r.min_steps_per_sec << ",";
    out << "\"max_steps_per_sec\":" << r.max_steps_per_sec << ",";
    out << "\"imbalance_pct\":" << r.imbalance_pct << ",";
    out << "\"batch_bytes\":" << (h2d_bytes + d2h_bytes) << ",";
    out << "\"h2d_bytes_per_step\":" << h2d_bytes << ",";
    out << "\"d2h_bytes_per_step\":" << d2h_bytes << ",";
    out << "\"compute_flops_per_step\":" << flops << ",";
    out << "\"config\":{";
    out << "\"m\":" << cfg.m << ",";
    out << "\"n\":" << cfg.n << ",";
    out << "\"k\":" << cfg.k << ",";
    out << "\"steps\":" << cfg.steps << ",";
    out << "\"samples\":" << cfg.samples << ",";
    out << "\"warmup_steps\":" << cfg.warmup_steps << "},";
    out << "\"error\":\"" << json_escape(r.error) << "\"}";
}

static void print_result(const std::vector<DeviceResult> &devices, const NetworkResult &net, const std::string &fatal, const GPUWorkloadConfig &workload_cfg, const GPUWorkloadResult &workload_result, const GPUTensorConfig &tensor_cfg, const GPUTensorResult &tensor_result) {
    double total_spec = 0.0, total_measured = 0.0;
    std::vector<std::string> gpu_errors;
    for (const auto &d : devices) {
        if (d.spec > 0.0) total_spec += d.spec;
        if (d.measured > 0.0) total_measured += d.measured;
        for (const auto &e : d.errors) gpu_errors.push_back("gpu" + std::to_string(d.index) + "=" + e);
    }
    if (!fatal.empty()) gpu_errors.push_back(fatal);
    std::string gpu_status = "success";
    if (!gpu_errors.empty()) gpu_status = devices.empty() ? "error" : "partial";
    if (devices.empty()) gpu_status = "error";

    std::ostringstream out;
    out << std::fixed << std::setprecision(3);
    out << RESULT_PREFIX << "{\"gpu\":{";
    out << "\"status\":\"" << gpu_status << "\",";
    out << "\"bench_backend\":\"cuda-native-v1\",";
    out << "\"cuda_build\":\"" << CUDART_VERSION << "\",";
    out << "\"num_gpus\":" << devices.size() << ",";
    out << "\"per_gpu_tflops\":[";
    for (size_t i = 0; i < devices.size(); ++i) { if (i) out << ','; out << devices[i].measured; }
    out << "],\"per_gpu_tflops_spec\":[";
    for (size_t i = 0; i < devices.size(); ++i) { if (i) out << ','; out << devices[i].spec; }
    out << "],\"per_gpu_vram_mb\":[";
    for (size_t i = 0; i < devices.size(); ++i) { if (i) out << ','; out << devices[i].vram_mb; }
    out << "],\"per_gpu_mem_bw_gbps\":[";
    for (size_t i = 0; i < devices.size(); ++i) { if (i) out << ','; out << devices[i].mem_bw; }
    out << "],\"total_spec_tflops\":" << total_spec << ",";
    out << "\"total_measured_tflops\":" << total_measured << ",";
    out << "\"devices\":[";
    for (size_t i = 0; i < devices.size(); ++i) {
        const auto &d = devices[i];
        if (i) out << ',';
        out << "{\"index\":" << d.index
            << ",\"name\":\"" << json_escape(d.name) << "\""
            << ",\"compute_capability\":\"" << d.major << "." << d.minor << "\""
            << ",\"sm_count\":" << d.sm_count
            << ",\"clock_khz\":" << d.clock_khz
            << ",\"vram_mb\":" << d.vram_mb
            << ",\"spec_tflops\":" << d.spec
            << ",\"measured_tflops\":" << d.measured
            << ",\"memory_bandwidth_gbps\":" << d.mem_bw
            << ",\"compat_status\":\"" << d.compat << "\""
            << ",\"stress_status\":\"" << d.stress << "\""
            << ",\"errors\":[";
        for (size_t j = 0; j < d.errors.size(); ++j) { if (j) out << ','; out << "\"" << json_escape(d.errors[j]) << "\""; }
        out << "]}";
    }
    out << "],\"compat\":{\"status\":\"";
    bool compat_error = false, compat_success = false;
    for (const auto &d : devices) { compat_error = compat_error || d.compat == "error"; compat_success = compat_success || d.compat == "success"; }
    out << (compat_error ? "partial" : (compat_success ? "success" : "skip")) << "\"},";
    out << "\"ecc\":{\"status\":\"skip\",\"per_gpu\":[],\"error\":\"nvml_disabled\"},";
    out << "\"stress\":{\"status\":\"";
    bool stress_error = false, stress_success = false;
    for (const auto &d : devices) { stress_error = stress_error || d.stress == "error"; stress_success = stress_success || d.stress == "success"; }
    out << (stress_error ? "partial" : (stress_success ? "success" : "skip")) << "\"},";
    out << "\"error\":\"" << json_escape(join_errors(gpu_errors)) << "\"},";
    out << "\"network\":{\"status\":\"" << net.status << "\",";
    out << "\"target_host\":\"" << json_escape(net.host) << "\",";
    out << "\"target_port\":" << net.port << ",";
    out << "\"seconds\":" << net.seconds << ",";
    out << "\"download_mbps\":" << net.down << ",";
    out << "\"upload_mbps\":" << net.up << ",";
    out << "\"error\":\"" << json_escape(net.error) << "\"},";
    print_gpu_workload(out, workload_cfg, workload_result);
    out << ",";
    print_gpu_tensor(out, tensor_cfg, tensor_result);
    out << "}";
    std::cout << out.str() << std::endl;
}

int main() {
    bool run_base = env_bool("BENCH_RUN_GPU_BASE", true);
    bool run_compat_flag = env_bool("BENCH_RUN_COMPAT", true);
    bool run_stress_flag = env_bool("BENCH_RUN_STRESS", true);
    bool run_network_flag = env_bool("BENCH_RUN_NETWORK", true);

    int count = 0;
    cudaError_t err = cudaGetDeviceCount(&count);
    std::vector<DeviceResult> devices;
    std::string fatal;
    if (err != cudaSuccess) {
        fatal = "cuda_get_device_count: " + cuda_error(err);
    } else if (count <= 0) {
        fatal = "no_cuda_devices";
    } else {
        for (int i = 0; i < count; ++i) devices.push_back(run_device(i, run_base, run_compat_flag, run_stress_flag));
    }
    GPUWorkloadConfig workload_cfg = gpu_workload_config();
    GPUWorkloadResult workload_result = run_gpu_workload(devices, workload_cfg, fatal);
    GPUTensorConfig tensor_cfg = gpu_tensor_config();
    GPUTensorResult tensor_result = run_gpu_tensor(devices, tensor_cfg, fatal);
    NetworkResult net = run_network(run_network_flag);
    print_result(devices, net, fatal, workload_cfg, workload_result, tensor_cfg, tensor_result);
    return 0;
}
