package hw

import (
	"testing"

	ghwgpu "github.com/jaypipes/ghw/pkg/gpu"
	ghwpci "github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
)

func TestNormalizePCIAddress(t *testing.T) {
	got := normalizePCIAddress("00000000:01:00.0")
	if got != "0000:01:00.0" {
		t.Fatalf("expected 0000:01:00.0, got %q", got)
	}
}

func TestShouldOverrideGPUProductName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "", want: true},
		{name: "unknown", want: true},
		{name: "Device [10de:2803]", want: true},
		{name: "NVIDIA Corporation Device [10de:2803]", want: true},
		{name: "NVIDIA GeForce RTX 4060 Ti", want: false},
		{name: "GA102 [GeForce RTX 3090]", want: false},
	}

	for _, tc := range cases {
		got := shouldOverrideGPUProductName(tc.name)
		if got != tc.want {
			t.Fatalf("name=%q expected %v got %v", tc.name, tc.want, got)
		}
	}
}

func TestReconcileRuntimeGPUs_ExcludesRaphaelIntegratedGPU(t *testing.T) {
	cards := []*ghwgpu.GraphicsCard{
		testGraphicsCard(0, "0000:01:00.0", "10de", "TU104 [GeForce RTX 2070 SUPER]"),
		testGraphicsCard(1, "0000:13:00.0", "1002", "Raphael"),
	}
	runtimeGPUs := []runtimeGPU{{
		Index:      0,
		PCIAddress: "00000000:01:00.0",
		UUID:       "GPU-2070",
		Name:       "NVIDIA GeForce RTX 2070 SUPER",
		VRAMMB:     8192,
		ComputeAPI: "CUDA 7.5",
	}}

	got := reconcileRuntimeGPUs(cards, runtimeGPUs)
	if len(got) != 1 {
		t.Fatalf("expected one rentable GPU, got %d", len(got))
	}
	if got[0].PCIAddress != "0000:01:00.0" || got[0].VRAMMB != 8192 {
		t.Fatalf("unexpected rentable GPU: %+v", got[0])
	}
	if got[0].Card.Index != 0 {
		t.Fatalf("expected normalized card index 0, got %d", got[0].Card.Index)
	}
}

func TestReconcileRuntimeGPUs_KeepsVerifiedMobileNVIDIA(t *testing.T) {
	cards := []*ghwgpu.GraphicsCard{
		testGraphicsCard(0, "0000:01:00.0", "10de", "AD107M [GeForce RTX 4060 Max-Q / Mobile]"),
	}
	runtimeGPUs := []runtimeGPU{{
		Index:      0,
		PCIAddress: "0000:01:00.0",
		UUID:       "GPU-mobile",
		Name:       "NVIDIA GeForce RTX 4060 Laptop GPU",
		VRAMMB:     8188,
		ComputeAPI: "CUDA 8.9",
	}}

	got := reconcileRuntimeGPUs(cards, runtimeGPUs)
	if len(got) != 1 {
		t.Fatalf("expected verified mobile GPU to remain rentable, got %d", len(got))
	}
	if got[0].UUID != "GPU-mobile" {
		t.Fatalf("unexpected GPU UUID: %q", got[0].UUID)
	}
}

func TestReconcileRuntimeGPUs_MatchesWindowsNVIDIAByUniqueName(t *testing.T) {
	card := testGraphicsCard(0, "VideoController1", "NVIDIA", "NVIDIA GeForce RTX 4060 Laptop GPU")
	card.DeviceInfo.Address = `PCI\\VEN_10DE&DEV_28E0`
	runtimeGPUs := []runtimeGPU{{
		Index:      0,
		PCIAddress: "00000000:01:00.0",
		UUID:       "GPU-windows",
		Name:       "NVIDIA GeForce RTX 4060 Laptop GPU",
		VRAMMB:     8188,
		ComputeAPI: "CUDA 8.9",
	}}

	got := reconcileRuntimeGPUs([]*ghwgpu.GraphicsCard{card}, runtimeGPUs)
	if len(got) != 1 {
		t.Fatalf("expected Windows NVIDIA GPU to match by name, got %d", len(got))
	}
	if got[0].PCIAddress != "0000:01:00.0" || got[0].Card.Address != "0000:01:00.0" {
		t.Fatalf("expected runtime PCI address, got %+v", got[0])
	}
}

func TestReconcileRuntimeGPUs_ExcludesUnverifiedAdapters(t *testing.T) {
	cards := []*ghwgpu.GraphicsCard{
		testGraphicsCard(0, "0000:01:00.0", "10de", "NVIDIA GeForce RTX 4060"),
		testGraphicsCard(1, "0000:13:00.0", "1002", "Raphael"),
	}

	if got := reconcileRuntimeGPUs(cards, nil); len(got) != 0 {
		t.Fatalf("expected no rentable GPUs without runtime evidence, got %d", len(got))
	}
}

func TestReconcileRuntimeGPUs_MatchesMultipleNVIDIAByPCIAddress(t *testing.T) {
	cards := []*ghwgpu.GraphicsCard{
		testGraphicsCard(0, "0000:01:00.0", "10de", "NVIDIA GPU A"),
		testGraphicsCard(1, "0000:02:00.0", "10de", "NVIDIA GPU B"),
	}
	runtimeGPUs := []runtimeGPU{
		{Index: 1, PCIAddress: "0000:01:00.0", VRAMMB: 12288, ComputeAPI: "CUDA 8.6"},
		{Index: 0, PCIAddress: "00000000:02:00.0", VRAMMB: 24576, ComputeAPI: "CUDA 8.9"},
	}

	got := reconcileRuntimeGPUs(cards, runtimeGPUs)
	if len(got) != 2 {
		t.Fatalf("expected two rentable GPUs, got %d", len(got))
	}
	if got[0].PCIAddress != "0000:02:00.0" || got[0].VRAMMB != 24576 || got[0].Card.Index != 0 {
		t.Fatalf("unexpected first GPU: %+v", got[0])
	}
	if got[1].PCIAddress != "0000:01:00.0" || got[1].VRAMMB != 12288 || got[1].Card.Index != 1 {
		t.Fatalf("unexpected second GPU: %+v", got[1])
	}
}

func TestParseNvidiaSMIGPUs(t *testing.T) {
	raw := []byte("0, 00000000:01:00.0, GPU-abc, NVIDIA GeForce RTX 2070 SUPER, 8192 MiB, 7.5\r\n")

	got, err := parseNvidiaSMIGPUs(raw)
	if err != nil {
		t.Fatalf("parse nvidia-smi output: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one GPU, got %d", len(got))
	}
	if got[0].PCIAddress != "00000000:01:00.0" || got[0].VRAMMB != 8192 || got[0].ComputeAPI != "CUDA 7.5" {
		t.Fatalf("unexpected parsed GPU: %+v", got[0])
	}
}

func testGraphicsCard(index int, address, vendorID, product string) *ghwgpu.GraphicsCard {
	return &ghwgpu.GraphicsCard{
		Index:   index,
		Address: address,
		DeviceInfo: &ghwpci.Device{
			Address: address,
			Vendor:  &pcidb.Vendor{ID: vendorID, Name: vendorID},
			Product: &pcidb.Product{Name: product},
		},
	}
}
