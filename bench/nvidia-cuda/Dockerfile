ARG CUDA_VERSION=12.8.1
ARG UBUNTU_VERSION=22.04

FROM nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS build
ARG CUTLASS_VERSION=v4.6.0
WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL "https://github.com/NVIDIA/cutlass/archive/refs/tags/${CUTLASS_VERSION}.tar.gz" -o /tmp/cutlass.tar.gz \
    && mkdir -p /opt/cutlass \
    && tar -xzf /tmp/cutlass.tar.gz --strip-components=1 -C /opt/cutlass \
    && rm -f /tmp/cutlass.tar.gz
COPY bench.cu /src/bench.cu
RUN mkdir -p /out && nvcc -O3 -std=c++17 \
    -I/opt/cutlass/include \
    -I/opt/cutlass/tools/util/include \
    -gencode=arch=compute_75,code=sm_75 \
    -gencode=arch=compute_80,code=sm_80 \
    -gencode=arch=compute_86,code=sm_86 \
    -gencode=arch=compute_89,code=sm_89 \
    -gencode=arch=compute_90,code=sm_90 \
    -gencode=arch=compute_100,code=sm_100 \
    -gencode=arch=compute_120,code=sm_120 \
    -gencode=arch=compute_120,code=compute_120 \
    -cudart=static \
    bench.cu -o /out/connect-bench-nvidia-cuda

FROM ubuntu:${UBUNTU_VERSION}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates iperf3 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/connect-bench-nvidia-cuda /usr/local/bin/connect-bench-nvidia-cuda
ENTRYPOINT []
CMD ["/usr/local/bin/connect-bench-nvidia-cuda"]
