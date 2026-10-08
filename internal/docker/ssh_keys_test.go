package docker

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

func TestNormalizeContainerSSHPublicKeys(t *testing.T) {
	got := normalizeContainerSSHPublicKeys([]string{
		"  ssh-ed25519 AAAA comment  ",
		"ssh-ed25519 AAAA comment",
		"",
		"ssh-rsa BBBB comment",
		"line1\nline2",
	})

	want := []string{
		"ssh-ed25519 AAAA comment",
		"ssh-rsa BBBB comment",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized keys: got=%#v want=%#v", got, want)
	}
}

func TestEnsureContainerSSHRuntime_ContainerVariants(t *testing.T) {
	origPrepare := prepareOpenSSHRuntimeFn
	origStartDropbear := tryStartDropbearRuntimeFn
	origInjectDropbear := tryInjectDropbearBinaryFn
	t.Cleanup(func() {
		prepareOpenSSHRuntimeFn = origPrepare
		tryStartDropbearRuntimeFn = origStartDropbear
		tryInjectDropbearBinaryFn = origInjectDropbear
	})

	testCases := []struct {
		name          string
		prepareExit   int
		prepareOut    string
		prepareErr    error
		startedNative bool
		nativeErr     error
		startedInject bool
		injectErr     error
		wantRuntime   sshRuntime
		wantErr       []string
		wantCalls     []string
	}{
		{
			name:        "ubuntu-openssh-available",
			prepareExit: 0,
			wantRuntime: sshRuntimeOpenSSH,
			wantCalls:   []string{"prepare"},
		},
		{
			name:          "alpine-dropbear-preinstalled",
			prepareExit:   sshdMissingExitCode,
			startedNative: true,
			wantRuntime:   sshRuntimeDropbear,
			wantCalls:     []string{"prepare", "start-native"},
		},
		{
			name:          "minimal-image-requires-injection",
			prepareExit:   sshdMissingExitCode,
			startedNative: false,
			startedInject: true,
			wantRuntime:   sshRuntimeDropbear,
			wantCalls:     []string{"prepare", "start-native", "inject"},
		},
		{
			name:          "broken-sshd-and-injection-fails",
			prepareExit:   sshDaemonInvalidConfigExitCode,
			startedNative: false,
			nativeErr:     fmt.Errorf("native dropbear failed"),
			startedInject: false,
			injectErr:     fmt.Errorf("read fallback dropbear binary failed"),
			wantErr: []string{
				"container does not provide sshd and fallback installer failed:",
				"openssh present but invalid/unstartable",
				"native dropbear failed",
				"read fallback dropbear binary failed",
			},
			wantCalls: []string{"prepare", "start-native", "inject"},
		},
		{
			name:        "prepare-openssh-exec-error",
			prepareErr:  fmt.Errorf("docker exec failed"),
			wantErr:     []string{"prepare ssh key-only config: docker exec failed"},
			wantCalls:   []string{"prepare"},
			wantRuntime: "",
		},
		{
			name:          "unexpected-openssh-exit-and-fallback-missing",
			prepareExit:   99,
			prepareOut:    "some fatal sshd output",
			startedNative: false,
			startedInject: false,
			wantErr: []string{
				"openssh prepare failed with exit code 99: some fatal sshd output",
			},
			wantCalls: []string{"prepare", "start-native", "inject"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			prepareOpenSSHRuntimeFn = func(_ context.Context, _ *client.Client, _ string) (int, string, error) {
				calls = append(calls, "prepare")
				return tc.prepareExit, tc.prepareOut, tc.prepareErr
			}
			tryStartDropbearRuntimeFn = func(_ context.Context, _ *client.Client, _ string, containerBinaryPath string) (bool, error) {
				if containerBinaryPath != "" {
					t.Fatalf("native dropbear start should receive empty binary path, got=%q", containerBinaryPath)
				}
				calls = append(calls, "start-native")
				return tc.startedNative, tc.nativeErr
			}
			tryInjectDropbearBinaryFn = func(_ context.Context, _ *client.Client, _ string) (bool, error) {
				calls = append(calls, "inject")
				return tc.startedInject, tc.injectErr
			}

			gotRuntime, err := ensureContainerSSHRuntime(context.Background(), nil, "container-1")
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("ensureContainerSSHRuntime returned error: %v", err)
				}
				if gotRuntime != tc.wantRuntime {
					t.Fatalf("unexpected runtime: got=%q want=%q", gotRuntime, tc.wantRuntime)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %v, got nil", tc.wantErr)
				}
				gotErr := err.Error()
				for _, want := range tc.wantErr {
					if !strings.Contains(gotErr, want) {
						t.Fatalf("error mismatch: got=%q missing=%q", gotErr, want)
					}
				}
			}

			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("unexpected call order: got=%v want=%v", calls, tc.wantCalls)
			}
		})
	}
}

func TestBuildDropbearStartScript_Variants(t *testing.T) {
	t.Run("use-container-dropbear-path", func(t *testing.T) {
		script := buildDropbearStartScript("")
		if !strings.Contains(script, "command -v dropbear") {
			t.Fatalf("expected script to probe dropbear from container image")
		}
		if !strings.Contains(script, "exit 44") {
			t.Fatalf("expected script to use missing-dropbear exit code")
		}
		if strings.Contains(script, fmt.Sprintf("DROPBEAR_BIN=%q", injectedDropbearContainerPath)) {
			t.Fatalf("script should not pin injected binary path when no path provided")
		}
	})

	t.Run("use-injected-dropbear-path", func(t *testing.T) {
		script := buildDropbearStartScript(injectedDropbearContainerPath)
		if !strings.Contains(script, fmt.Sprintf("DROPBEAR_BIN=%q", injectedDropbearContainerPath)) {
			t.Fatalf("expected script to pin injected binary path")
		}
		if !strings.Contains(script, "\"$DROPBEAR_BIN\" -R -E -p 22 -P /var/run/dropbear.pid -s -g") {
			t.Fatalf("expected script to start dropbear with hardened flags")
		}
	})
}

func TestDropbearManifestMatch_CaseInsensitiveAndTrimmed(t *testing.T) {
	manifest := dropbearManifest{
		Binaries: []dropbearManifestBinary{
			{OS: " linux ", Arch: " ARM64 ", URL: "https://example.com/dropbear"},
		},
	}
	got, ok := manifest.match("linux", "arm64")
	if !ok {
		t.Fatalf("expected manifest item match")
	}
	if got.URL != "https://example.com/dropbear" {
		t.Fatalf("unexpected match payload: %#v", got)
	}
}
