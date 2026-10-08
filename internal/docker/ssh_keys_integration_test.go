//go:build integration

package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	testTargetPlatform = "linux/amd64"
	testTargetOS       = "linux"
	testTargetArch     = "amd64"
)

func TestApplyContainerSSHPublicKeys_Integration(t *testing.T) {
	if os.Getenv("RUN_DOCKER_INTEGRATION") != "1" {
		t.Skip("set RUN_DOCKER_INTEGRATION=1 to run Docker integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create docker client: %v", err)
	}
	defer cli.Close()

	fakeDropbearPath := buildFakeDropbearBinary(t)

	for _, ref := range []string{
		"alpine:3.20",
		"busybox:1.36.1",
		"gcr.io/distroless/static-debian12:latest",
	} {
		if err := pullImageForPlatform(ctx, cli, ref, testTargetPlatform); err != nil {
			t.Fatalf("pull image %s: %v", ref, err)
		}
	}

	origCandidates := hardcodedDropbearBinaryCandidates
	origManifestURL := dropbearManifestURL
	hardcodedDropbearBinaryCandidates = []string{fakeDropbearPath}
	dropbearManifestURL = ""
	t.Cleanup(func() {
		hardcodedDropbearBinaryCandidates = origCandidates
		dropbearManifestURL = origManifestURL
	})

	svc := NewService()
	keys := []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey comment"}

	testCases := []struct {
		name              string
		imageRef          string
		cmd               []string
		entrypoint        []string
		binds             []string
		hasShell          bool
		expectApplyErr    bool
		errorContains     []string
		expectInjectedBin bool
		expectProbeOK     bool
	}{
		{
			name:              "alpine-minimal-injection",
			imageRef:          "alpine:3.20",
			cmd:               []string{"sh", "-lc", "while true; do sleep 3600; done"},
			hasShell:          true,
			expectInjectedBin: true,
			expectProbeOK:     true,
		},
		{
			name:              "busybox-minimal-injection",
			imageRef:          "busybox:1.36.1",
			cmd:               []string{"sh", "-lc", "while true; do sleep 3600; done"},
			hasShell:          true,
			expectInjectedBin: true,
			expectProbeOK:     true,
		},
		{
			name:              "alpine-native-dropbear-mounted-no-injection",
			imageRef:          "alpine:3.20",
			cmd:               []string{"sh", "-lc", "while true; do sleep 3600; done"},
			binds:             []string{fmt.Sprintf("%s:/usr/sbin/dropbear:ro", fakeDropbearPath)},
			hasShell:          true,
			expectInjectedBin: false,
			expectProbeOK:     true,
		},
		{
			name:           "distroless-no-shell-fails",
			imageRef:       "gcr.io/distroless/static-debian12:latest",
			entrypoint:     []string{"/dropbear-host"},
			binds:          []string{fmt.Sprintf("%s:/dropbear-host:ro", fakeDropbearPath)},
			expectApplyErr: true,
			errorContains: []string{
				"fallback installer failed",
				"exec: \"/bin/sh\": stat /bin/sh: no such file or directory",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			containerID := createManagedRunningContainer(ctx, t, cli, tc.imageRef, tc.cmd, tc.entrypoint, tc.binds)
			t.Cleanup(func() {
				_ = cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			})

			err := svc.ApplyContainerSSHPublicKeys(ctx, containerID, keys)
			if tc.expectApplyErr {
				if err == nil {
					t.Fatalf("expected apply error for %s", tc.name)
				}
				got := err.Error()
				for _, want := range tc.errorContains {
					if !strings.Contains(got, want) {
						t.Fatalf("unexpected apply error, missing %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("apply container ssh public keys: %v", err)
			}

			if tc.hasShell {
				if err := assertAuthorizedKeysPresent(ctx, cli, containerID, keys[0]); err != nil {
					t.Fatalf("authorized_keys validation failed: %v", err)
				}

				injected, err := fileExistsInContainer(ctx, cli, containerID, injectedDropbearContainerPath)
				if err != nil {
					t.Fatalf("check injected dropbear path: %v", err)
				}
				if injected != tc.expectInjectedBin {
					t.Fatalf("unexpected injected binary presence: got=%v want=%v", injected, tc.expectInjectedBin)
				}
			}

			if tc.expectProbeOK {
				probe, err := svc.ProbeContainerSSH(ctx, containerID)
				if err != nil {
					t.Fatalf("probe ssh: %v", err)
				}
				if !probe.Available {
					t.Fatalf("expected probe.Available=true, got false reason=%q ip=%q", probe.Reason, probe.ContainerIP)
				}
			}
		})
	}
}

func pullImageForPlatform(ctx context.Context, cli *client.Client, ref string, platform string) error {
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{Platform: platform})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc)
	return nil
}

func createManagedRunningContainer(
	ctx context.Context,
	t *testing.T,
	cli *client.Client,
	imageRef string,
	cmd []string,
	entrypoint []string,
	binds []string,
) string {
	t.Helper()

	name := fmt.Sprintf("swarmind-ssh-int-%d", time.Now().UnixNano())
	cfg := &container.Config{
		Image:  imageRef,
		Labels: map[string]string{"managed-by": "template-manager"},
		Cmd:    cmd,
	}
	if len(entrypoint) > 0 {
		cfg.Entrypoint = entrypoint
	}

	hostCfg := &container.HostConfig{Binds: binds}
	platform := &ocispec.Platform{OS: testTargetOS, Architecture: testTargetArch}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, platform, name)
	if err != nil {
		t.Fatalf("container create (%s): %v", imageRef, err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("container start (%s): %v", imageRef, err)
	}

	if err := waitContainerRunning(ctx, cli, resp.ID); err != nil {
		t.Fatalf("container did not reach running state: %v", err)
	}

	return resp.ID
}

func waitContainerRunning(ctx context.Context, cli *client.Client, containerID string) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		insp, err := cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return err
		}
		if insp.State != nil && insp.State.Running {
			return nil
		}
		if time.Now().After(deadline) {
			status := "unknown"
			if insp.State != nil {
				status = insp.State.Status
			}
			return fmt.Errorf("timeout waiting for running state, current=%s", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assertAuthorizedKeysPresent(ctx context.Context, cli *client.Client, containerID string, wantLine string) error {
	exitCode, output, err := execInContainer(ctx, cli, containerID, []string{
		"/bin/sh",
		"-lc",
		fmt.Sprintf("test -f /root/.ssh/authorized_keys && grep -F %q /root/.ssh/authorized_keys", wantLine),
	})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("authorized_keys assertion failed with exit=%d output=%s", exitCode, strings.TrimSpace(output))
	}
	return nil
}

func fileExistsInContainer(ctx context.Context, cli *client.Client, containerID string, filePath string) (bool, error) {
	exitCode, output, err := execInContainer(ctx, cli, containerID, []string{
		"/bin/sh",
		"-lc",
		fmt.Sprintf("test -x %q", filePath),
	})
	if err != nil {
		return false, err
	}
	if exitCode == 0 {
		return true, nil
	}
	if exitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("unexpected exit %d while checking %s: %s", exitCode, filePath, strings.TrimSpace(output))
}

func buildFakeDropbearBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "main.go")
	outPath := filepath.Join(tmp, "dropbear-fake")

	source := `package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

func main() {
	pidPath := "/var/run/dropbear.pid"
	for i := 0; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-P" {
			pidPath = os.Args[i+1]
			i++
		}
	}

	_ = os.MkdirAll(filepath.Dir(pidPath), 0o755)
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644)

	ln, err := net.Listen("tcp", ":22")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	for {
		sig := <-sigs
		if sig == syscall.SIGHUP {
			continue
		}
		_ = ln.Close()
		<-done
		return
	}
}
`

	if err := os.WriteFile(srcPath, []byte(source), 0o644); err != nil {
		t.Fatalf("write fake dropbear source: %v", err)
	}

	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", outPath, srcPath)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+testTargetOS,
		"GOARCH="+testTargetArch,
	)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		t.Fatalf("build fake dropbear binary: %v\n%s", err, combined.String())
	}

	if err := os.Chmod(outPath, 0o755); err != nil {
		t.Fatalf("chmod fake dropbear binary: %v", err)
	}

	return outPath
}
