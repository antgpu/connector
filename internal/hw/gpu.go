//go:build cgo

package hw

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/jaypipes/ghw"
)

func FetchGPUInfo(s *SystemSpecs) error {
	gpu, err := ghw.GPU()
	if err != nil {
		return fmt.Errorf("fetchGPUInfo: %w", err)
	}

	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("initialize NVML: %v", nvml.ErrorString(ret))
	}
	defer nvml.Shutdown()

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get device count: %v", nvml.ErrorString(ret))
	}

	runtimeGPUs := make([]runtimeGPU, 0, count)
	// TODO: other platforms
	for i := range count {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		pciInfo, ret := device.GetPciInfo()
		if ret != nvml.SUCCESS {
			continue
		}
		address := normalizeNVMLBusID(pciInfo.BusId)
		if address == "" {
			continue
		}

		memInfo, ret := device.GetMemoryInfo()
		if ret != nvml.SUCCESS || memInfo.Total == 0 {
			continue
		}

		major, minor, ret := device.GetCudaComputeCapability()
		if ret != nvml.SUCCESS {
			continue
		}

		name, _ := device.GetName()
		uuid, _ := device.GetUUID()
		runtimeGPUs = append(runtimeGPUs, runtimeGPU{
			Index:      i,
			PCIAddress: address,
			UUID:       strings.TrimSpace(uuid),
			Name:       strings.TrimSpace(name),
			VRAMMB:     memInfo.Total / 1024 / 1024,
			ComputeAPI: fmt.Sprintf("CUDA %d.%d", major, minor),
		})

		// TODO: measure tflops using wgpu shader tiled GEMM
	}

	applyRentableGPUs(s, reconcileRuntimeGPUs(gpu.GraphicsCards, runtimeGPUs))

	return nil
}

func normalizeNVMLBusID(raw [32]uint8) string {
	n := 0
	for ; n < len(raw); n++ {
		if raw[n] == 0 {
			break
		}
	}
	if n == 0 {
		return ""
	}
	return normalizePCIAddress(string(raw[:n]))
}
