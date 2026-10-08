//go:build windows && !cgo

package hw

import (
	"fmt"

	"github.com/jaypipes/ghw"
)

func FetchGPUInfo(s *SystemSpecs) error {
	gpu, err := ghw.GPU()
	if err != nil {
		return fmt.Errorf("fetchGPUInfo: %w", err)
	}

	runtimeGPUs, err := queryNvidiaSMIGPUs()
	if err != nil {
		applyRentableGPUs(s, nil)
		return fmt.Errorf("query nvidia-smi: %w", err)
	}
	applyRentableGPUs(s, reconcileRuntimeGPUs(gpu.GraphicsCards, runtimeGPUs))
	return nil
}
