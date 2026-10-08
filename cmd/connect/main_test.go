package main

import (
	"os"
	"testing"
	"time"

	"github.com/antgpu/connector/internal/hw"
)

func TestShouldRunAutoUpdaterMatchesBuildMode(t *testing.T) {
	want := true
	if got := shouldRunAutoUpdater(runtimeOptions{}); got != want {
		t.Fatalf("auto-updater enabled=%t, want %t", got, want)
	}
	if got := shouldRunAutoUpdater(runtimeOptions{TestAutoUpdate: true}); got != want {
		t.Fatalf("auto-updater with test flag enabled=%t, want %t", got, want)
	}
}

func TestEffectiveBenchRunModeMatchesBuildMode(t *testing.T) {
	got := effectiveBenchRunMode(hw.BenchRunMode{})
	if !got.RunGPUBase || !got.RunCompat || !got.RunStress {
		t.Fatalf("normal build must retain hardened benchmark groups: %+v", got)
	}
}

func TestBenchTimeout(t *testing.T) {
	t.Setenv("CONNECT_BENCH_TIMEOUT", "")
	if got := benchTimeout(); got != 2*time.Hour {
		t.Fatalf("unexpected default bench timeout: %v", got)
	}

	t.Setenv("CONNECT_BENCH_TIMEOUT", "45m")
	if got := benchTimeout(); got != 45*time.Minute {
		t.Fatalf("unexpected configured bench timeout: %v", got)
	}

	t.Setenv("CONNECT_BENCH_TIMEOUT", "bad")
	if got := benchTimeout(); got != 2*time.Hour {
		t.Fatalf("unexpected fallback bench timeout: %v", got)
	}
}

func TestShouldEnforceBenchSuccessProdAlwaysStrict(t *testing.T) {
	if devBuild {
		t.Skip("prod-only assertion")
	}
	t.Setenv("SWARMIND_CONNECT_ALLOW_BENCH_SKIP", "1")
	if !shouldEnforceBenchSuccess() {
		t.Fatal("expected prod build to enforce bench success")
	}
}

func TestIsTunnelCertTimeError(t *testing.T) {
	if !isTunnelCertTimeError(&errString{"nebula certificate for this host is expired"}) {
		t.Fatal("expected nebula expired certificate error to be retryable")
	}
	if isTunnelCertTimeError(&errString{"udp handshake timeout"}) {
		t.Fatal("expected non-cert tunnel error not to be retryable")
	}
}

type errString struct{ s string }

func (e *errString) Error() string { return e.s }

func TestIsManagedServiceRuntime(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	if isManagedServiceRuntime(runtimeOptions{}) {
		t.Fatal("expected unmanaged runtime without service markers")
	}
	if !isManagedServiceRuntime(runtimeOptions{RunningAsService: true}) {
		t.Fatal("expected managed runtime when RunningAsService is set")
	}
	t.Setenv("INVOCATION_ID", "abc123")
	if !isManagedServiceRuntime(runtimeOptions{}) {
		t.Fatal("expected managed runtime under systemd invocation")
	}
}

func TestHasPersistedEnrollmentState(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	got, err := hasPersistedEnrollmentState()
	if err != nil {
		t.Fatalf("hasPersistedEnrollmentState empty: %v", err)
	}
	if got {
		t.Fatal("expected no persisted state in empty dir")
	}
	if err := os.WriteFile(MachineAuthPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	got, err = hasPersistedEnrollmentState()
	if err != nil {
		t.Fatalf("hasPersistedEnrollmentState with auth: %v", err)
	}
	if !got {
		t.Fatal("expected persisted state when auth file exists")
	}
}
