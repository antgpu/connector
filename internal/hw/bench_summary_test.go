package hw

import (
	"strings"
	"testing"
)

func TestBenchSummaryNetworkOnlyOmitsTFLOPS(t *testing.T) {
	mode := BenchRunMode{RunNetwork: true}
	out := benchRunnerOutput{}
	out.Network.Status = "success"
	out.GPU.TotalMeasuredTFLOPS = 123.45
	out.GPU.TotalSpecTFLOPS = 234.56

	summary := benchSummary(mode, out)
	if !strings.Contains(summary, `network_status="success"`) {
		t.Fatalf("expected network status in summary, got %q", summary)
	}
	if strings.Contains(summary, "tflops") {
		t.Fatalf("did not expect TFLOPS fields when gpu_base is disabled, got %q", summary)
	}
}

func TestBenchSummaryIncludesRequestedGPUStatuses(t *testing.T) {
	mode := BenchRunMode{RunCompat: true, RunECC: true, RunStress: true}
	out := benchRunnerOutput{}
	out.GPU.Status = "success"
	out.GPU.Compat = map[string]any{"status": "partial"}
	out.GPU.ECC = map[string]any{"status": "success"}
	out.GPU.Stress = map[string]any{"status": "skip"}

	summary := benchSummary(mode, out)
	expect := []string{
		`gpu_status="success"`,
		`compat_status="partial"`,
		`ecc_status="success"`,
		`stress_status="skip"`,
	}
	for _, token := range expect {
		if !strings.Contains(summary, token) {
			t.Fatalf("expected %q in summary %q", token, summary)
		}
	}
	if strings.Contains(summary, "total_measured_tflops") || strings.Contains(summary, "total_spec_tflops") {
		t.Fatalf("did not expect TFLOPS totals without gpu_base, got %q", summary)
	}
}
