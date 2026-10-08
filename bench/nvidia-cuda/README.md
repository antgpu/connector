# CUDA-native NVIDIA benchmark

Small CUDA C++ benchmark image for RTX 30/40/50 validation.

Primary workload uses NVIDIA CUTLASS SGEMM kernels with fixed H2D A/B → SGEMM → D2H probe → sync steps. CUTLASS is fetched and compiled into the image at build time; provider hosts only pull the pinned runtime image.

Build:

```bash
docker build -t connect-bench-nvidia-cuda:local bench/nvidia-cuda
```

Run:

```bash
docker run --rm --gpus all connect-bench-nvidia-cuda:local
```

Connector production path uses this benchmark directly; provider-local environment variables do not select benchmark variant or workload dimensions.

Output keeps connector contract:

```text
BENCH_RESULT={"gpu":...,"network":...,"gpu_workload":...}
```

Primary workload metric:

```text
gpu_workload.score
gpu_workload.score_unit = steps_per_sec
```
