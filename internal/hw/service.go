package hw

import (
	"fmt"
	"log"
	"strings"

	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/block"
	"github.com/jaypipes/ghw/pkg/cpu"
	"github.com/jaypipes/ghw/pkg/gpu"
	"github.com/jaypipes/ghw/pkg/memory"
	"github.com/jaypipes/ghw/pkg/net"
	"github.com/jaypipes/ghw/pkg/pci"

	psDisk "github.com/shirou/gopsutil/v4/disk"
	psNet "github.com/shirou/gopsutil/v4/net"
)

type SystemSpecs struct {
	BenchmarkProtocol   int                 `json:"benchmark_protocol,omitempty"`
	GraphicsCards       []*gpu.GraphicsCard `json:"cards"`
	RentableGPUs        []RentableGPU       `json:"rentable_gpus"`
	GPUVRAMMB           []uint64            `json:"gpu_vram_mb"`
	PerGPUTFLOPS        []float64           `json:"per_gpu_tflops"`
	PerGPUSpecTFLOPS    []float64           `json:"per_gpu_tflops_spec"`
	PerGPUMemBWGBPS     []float64           `json:"per_gpu_mem_bw_gbps"`
	TotalSpecTFLOPS     float64             `json:"total_spec_tflops"`
	TotalMeasuredTFLOPS float64             `json:"total_measured_tflops"`
	GPUCompat           map[string]any      `json:"gpu_compat"`
	GPUECC              map[string]any      `json:"gpu_ecc"`
	GPUStress           map[string]any      `json:"gpu_stress"`
	GPUWorkload         map[string]any      `json:"gpu_workload,omitempty"`
	GPUTensor           map[string]any      `json:"gpu_tensor,omitempty"`
	ComputeAPIs         []string            `json:"compute_apis"`
	GPUFetchErr         string              `json:"gpu_fetch_error"`
	BenchFetchErr       string              `json:"bench_fetch_error"`

	PCI         []*pci.Device `json:"pci"`
	PCIFetchErr string        `json:"pci_fetch_error"`

	CPU         *cpu.Info `json:"cpu"`
	CPUFetchErr string    `json:"cpu_fetch_error"`

	Memory         *memory.Info `json:"memory"`
	MemoryFetchErr string       `json:"memory_fetch_error"`

	Disks            []*block.Disk      `json:"disks"`
	DisksTotalSizeMB uint64             `json:"disks_total_size_mb"`
	DisksUsedSizeMB  uint64             `json:"disks_used_size_mb"`
	DisksFreeSizeMB  uint64             `json:"disks_free_size_mb"`
	DisksUsageErr    string             `json:"disks_usage_error,omitempty"`
	DisksFetchErr    string             `json:"disks_fetch_error"`
	DockerStorage    *DockerStorageInfo `json:"docker_storage,omitempty"`

	Network         *net.Info `json:"network"`
	NetworkFetchErr string    `json:"network_fetch_err"`

	NetworkPSUtil         psNet.InterfaceStatList `json:"network_psutil"`
	NetworkPsutilFetchErr string                  `json:"network_psutil_fetch_err"`
	NetDownMbps           int                     `json:"net_down_mbps"`
	NetUpMbps             int                     `json:"net_up_mbps"`
	ModelRunner           *ModelRunnerCapability  `json:"model_runner,omitempty"`
}

type RentableGPU struct {
	Card         *gpu.GraphicsCard `json:"card"`
	Runtime      string            `json:"runtime"`
	RuntimeIndex int               `json:"runtime_index"`
	UUID         string            `json:"uuid,omitempty"`
	PCIAddress   string            `json:"pci_address"`
	VRAMMB       uint64            `json:"vram_mb"`
	ComputeAPI   string            `json:"compute_api"`
}

type ModelRunnerCapability struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Healthy   bool   `json:"healthy"`
	BaseURL   string `json:"base_url,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func FetchSpecs() *SystemSpecs {

	specs := &SystemSpecs{}

	if err := FetchPCIInfo(specs); err != nil {
		specs.PCIFetchErr = err.Error()
		log.Println(err)
	}
	if err := FetchGPUInfo(specs); err != nil {
		specs.GPUFetchErr = err.Error()
		log.Println(err)
	}
	if err := FetchCPUInfo(specs); err != nil {
		specs.CPUFetchErr = err.Error()
		log.Println(err)
	}
	if err := FetchMemoryInfo(specs); err != nil {
		specs.MemoryFetchErr = err.Error()
		log.Println(err)
	}
	if err := FetchStorageInfo(specs); err != nil {
		specs.DisksFetchErr = err.Error()
		log.Println(err)
	}
	FetchNetworkInfo(specs)

	return specs
}

func FetchPCIInfo(s *SystemSpecs) error {
	pciInfo, err := ghw.PCI()
	if err != nil {
		return fmt.Errorf("fetchPCIInfo: %w", err)
	}
	s.PCI = pciInfo.Devices

	return nil
}

func FetchCPUInfo(s *SystemSpecs) error {
	var err error
	s.CPU, err = ghw.CPU()
	if err != nil {
		return fmt.Errorf("fetchCPUInfo: %w", err)
	}

	// TODO cpu benchmark

	return nil
}

func FetchStorageInfo(s *SystemSpecs) error {
	block, err := ghw.Block()
	if err != nil {
		return fmt.Errorf("fetchStorageInfo: %w", err)
	}

	s.DisksTotalSizeMB = block.TotalSizeBytes / 1024 / 1024 // MB
	s.Disks = block.Disks
	fillDiskUsage(s)
	s.DockerStorage = FetchDockerStorageInfo()

	// TODO disks speedtest

	return nil
}

func fillDiskUsage(s *SystemSpecs) {
	if s == nil {
		return
	}

	mountpoints := make([]string, 0, 8)
	seen := make(map[string]struct{})
	for _, d := range s.Disks {
		if d == nil {
			continue
		}
		for _, p := range d.Partitions {
			if p == nil {
				continue
			}
			mp := strings.TrimSpace(p.MountPoint)
			if mp == "" {
				continue
			}
			if _, ok := seen[mp]; ok {
				continue
			}
			seen[mp] = struct{}{}
			mountpoints = append(mountpoints, mp)
		}
	}

	var (
		totalBytes uint64
		freeBytes  uint64
		errCount   int
	)

	for _, mp := range mountpoints {
		usage, err := psDisk.Usage(mp)
		if err != nil {
			errCount++
			continue
		}
		totalBytes += usage.Total
		freeBytes += usage.Free
	}

	usedBytes := totalBytes
	if freeBytes <= totalBytes {
		usedBytes = totalBytes - freeBytes
	}
	s.DisksUsedSizeMB = usedBytes / 1024 / 1024
	s.DisksFreeSizeMB = freeBytes / 1024 / 1024
	if errCount > 0 {
		s.DisksUsageErr = fmt.Sprintf("disk usage probes failed for %d mountpoints", errCount)
	}
}

func FetchMemoryInfo(s *SystemSpecs) error {
	var err error
	s.Memory, err = ghw.Memory()
	if err != nil {
		return fmt.Errorf("fetchMemoryInfo: %w", err)
	}

	// TODO RAM speedtest

	return nil
}
