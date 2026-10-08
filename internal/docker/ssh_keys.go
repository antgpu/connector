package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/antgpu/connector/internal/securefs"
	pkgdocker "github.com/antgpu/connector/pkg/docker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const (
	sshdMissingExitCode            = 42
	sshDaemonInvalidConfigExitCode = 43
	dropbearMissingExitCode        = 44
	dropbearStartFailedExitCode    = 45

	injectedDropbearContainerPath          = "/tmp/.antgpu/dropbear"
	defaultDropbearFetchTimeout            = 20 * time.Second
	defaultDropbearAutofetchCacheSubfolder = "cache/dropbear"
	dropbearTargetOS                       = "linux"
	maxDropbearFetchBytes                  = 64 * 1024 * 1024
)

type sshRuntime string

const (
	sshRuntimeOpenSSH  sshRuntime = "openssh"
	sshRuntimeDropbear sshRuntime = "dropbear"
)

var dropbearAutofetchMu sync.Mutex
var dropbearManifestURL = defaultDropbearManifestURL
var dropbearCacheRoot = defaultDropbearAutofetchCacheSubfolder
var prepareOpenSSHRuntimeFn = prepareOpenSSHRuntime
var tryStartDropbearRuntimeFn = tryStartDropbearRuntime
var tryInjectDropbearBinaryFn = tryInjectDropbearBinary

var hardcodedDropbearBinaryCandidates = []string{
	"/opt/swarmind/dropbear/dropbear",
	"/usr/local/lib/swarmind/dropbear",
}

type dropbearManifest struct {
	Version  string                   `json:"version"`
	Binaries []dropbearManifestBinary `json:"binaries"`
}

type dropbearManifestBinary struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

func (s *Service) ApplyContainerSSHPublicKeys(ctx context.Context, containerID string, publicKeys []string) error {
	keys := normalizeContainerSSHPublicKeys(publicKeys)
	if len(keys) == 0 {
		return fmt.Errorf("public_keys must contain at least one key")
	}

	cli, err := s.newClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	if _, err := pkgdocker.InspectContainer(ctx, cli, containerID); err != nil {
		return fmt.Errorf("inspect managed container: %w", err)
	}

	runtimeKind, err := ensureContainerSSHRuntime(ctx, cli, containerID)
	if err != nil {
		return err
	}

	keysData := strings.Join(keys, "\n") + "\n"
	if err := copyFileToContainer(ctx, cli, containerID, "/root/.ssh/authorized_keys", []byte(keysData), 0o600); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	if err := finalizeContainerSSHKeys(ctx, cli, containerID, runtimeKind); err != nil {
		return err
	}

	return nil
}

func ensureContainerSSHRuntime(ctx context.Context, cli *client.Client, containerID string) (sshRuntime, error) {
	exitCode, output, err := prepareOpenSSHRuntimeFn(ctx, cli, containerID)
	if err != nil {
		return "", fmt.Errorf("prepare ssh key-only config: %w", err)
	}
	if exitCode == 0 {
		return sshRuntimeOpenSSH, nil
	}

	fallbackErrors := make([]string, 0, 3)
	if exitCode == sshdMissingExitCode {
		fallbackErrors = append(fallbackErrors, "openssh missing in container")
	} else if exitCode == sshDaemonInvalidConfigExitCode {
		fallbackErrors = append(fallbackErrors, "openssh present but invalid/unstartable")
	} else {
		fallbackErrors = append(fallbackErrors, fmt.Sprintf("openssh prepare failed with exit code %d: %s", exitCode, compactExecOutput(output)))
	}

	started, err := tryStartDropbearRuntimeFn(ctx, cli, containerID, "")
	if err != nil {
		fallbackErrors = append(fallbackErrors, err.Error())
	}
	if started {
		return sshRuntimeDropbear, nil
	}

	started, err = tryInjectDropbearBinaryFn(ctx, cli, containerID)
	if err != nil {
		fallbackErrors = append(fallbackErrors, err.Error())
	}
	if started {
		return sshRuntimeDropbear, nil
	}

	return "", fmt.Errorf("container does not provide sshd and fallback installer failed: %s", strings.Join(fallbackErrors, "; "))
}

func prepareOpenSSHRuntime(ctx context.Context, cli *client.Client, containerID string) (int, string, error) {
	prepareScript := strings.Join([]string{
		"set -eu",
		"SSHD_BIN=\"$(command -v sshd 2>/dev/null || true)\"",
		"if [ -z \"$SSHD_BIN\" ]; then SSHD_BIN=\"/usr/sbin/sshd\"; fi",
		"if [ ! -x \"$SSHD_BIN\" ]; then exit 42; fi",
		"mkdir -p /root/.ssh",
		"chmod 700 /root/.ssh",
		"mkdir -p /etc/ssh/sshd_config.d",
		"cat >/etc/ssh/sshd_config.d/99-antgpu-keyonly.conf <<'EOCONF'",
		"PermitRootLogin prohibit-password",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"ChallengeResponseAuthentication no",
		"PubkeyAuthentication yes",
		"EOCONF",
		"if [ -f /etc/ssh/sshd_config ]; then",
		"  grep -q 'PermitRootLogin prohibit-password' /etc/ssh/sshd_config || cat >>/etc/ssh/sshd_config <<'EOCONF'",
		"PermitRootLogin prohibit-password",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"ChallengeResponseAuthentication no",
		"PubkeyAuthentication yes",
		"EOCONF",
		"fi",
		"if command -v ssh-keygen >/dev/null 2>&1; then ssh-keygen -A >/dev/null 2>&1 || true; fi",
		"\"$SSHD_BIN\" -t >/dev/null 2>&1 || exit 43",
		"if ! awk '($2 ~ /:0016$/ && $4 == \"0A\") {found=1} END {exit(found?0:1)}' /proc/net/tcp /proc/net/tcp6 2>/dev/null; then",
		"  mkdir -p /var/run/sshd /run/sshd",
		"  \"$SSHD_BIN\" >/dev/null 2>&1 || \"$SSHD_BIN\" -D >/dev/null 2>&1 &",
		"fi",
	}, "\n")

	return execInContainer(ctx, cli, containerID, []string{"/bin/sh", "-lc", prepareScript})
}

func tryStartDropbearRuntime(ctx context.Context, cli *client.Client, containerID string, containerBinaryPath string) (bool, error) {
	script := buildDropbearStartScript(containerBinaryPath)
	exitCode, output, err := execInContainer(ctx, cli, containerID, []string{"/bin/sh", "-lc", script})
	if err != nil {
		return false, fmt.Errorf("start dropbear runtime: %w", err)
	}
	if exitCode == dropbearMissingExitCode {
		return false, nil
	}
	if exitCode != 0 {
		return false, fmt.Errorf("start dropbear failed with exit code %d: %s", exitCode, compactExecOutput(output))
	}
	return true, nil
}

func tryInjectDropbearBinary(ctx context.Context, cli *client.Client, containerID string) (bool, error) {
	hostBinaryPath, err := resolveDropbearFallbackBinary(ctx)
	if err != nil {
		return false, err
	}
	if hostBinaryPath == "" {
		return false, nil
	}

	payload, err := os.ReadFile(hostBinaryPath)
	if err != nil {
		return false, fmt.Errorf("read fallback dropbear binary from %s: %w", hostBinaryPath, err)
	}
	if len(payload) == 0 {
		return false, fmt.Errorf("read fallback dropbear binary from %s: empty payload", hostBinaryPath)
	}

	if err := copyFileToContainer(ctx, cli, containerID, injectedDropbearContainerPath, payload, 0o755); err != nil {
		return false, fmt.Errorf("inject dropbear binary: %w", err)
	}

	started, err := tryStartDropbearRuntime(ctx, cli, containerID, injectedDropbearContainerPath)
	if err != nil {
		return false, fmt.Errorf("start injected dropbear runtime: %w", err)
	}
	if !started {
		return false, fmt.Errorf("injected dropbear runtime not available")
	}

	return true, nil
}

func resolveDropbearFallbackBinary(ctx context.Context) (string, error) {
	for _, candidate := range hardcodedDropbearBinaryCandidates {
		path := strings.TrimSpace(candidate)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
			continue
		}
		if err := securefs.RegularFile(path); err != nil {
			return "", err
		}
		return path, nil
	}
	return autoFetchDropbearBinary(ctx)
}

func autoFetchDropbearBinary(ctx context.Context) (string, error) {
	dropbearAutofetchMu.Lock()
	defer dropbearAutofetchMu.Unlock()

	manifest, err := fetchDropbearManifest(ctx)
	if err != nil {
		return "", err
	}

	artifact, ok := manifest.match(dropbearTargetOS, runtime.GOARCH)
	if !ok {
		return "", nil
	}

	version := strings.TrimSpace(manifest.Version)
	if err := securefs.ValidateVersion(version); err != nil {
		return "", err
	}
	platformToken := dropbearTargetOS + "-" + runtime.GOARCH
	binaryPath := filepath.Join(dropbearCacheRoot, version, platformToken, "dropbear")
	if err := securefs.PrivateDir(filepath.Dir(binaryPath)); err != nil {
		return "", fmt.Errorf("secure dropbear cache: %w", err)
	}
	if _, err := os.Lstat(binaryPath); err == nil {
		if err := securefs.RegularFile(binaryPath); err != nil {
			return "", err
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return "", err
		}
		if err := validateDropbearSHA256(data, artifact.SHA256); err == nil {
			return binaryPath, nil
		}
		if err := os.Remove(binaryPath); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	data, err := fetchDropbearArtifact(ctx, artifact.URL, dropbearManifestURL)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("autofetch dropbear from %s: empty response", artifact.URL)
	}

	if err := validateDropbearSHA256(data, artifact.SHA256); err != nil {
		return "", err
	}

	temp, err := os.CreateTemp(filepath.Dir(binaryPath), "dropbear-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if err == nil {
		err = temp.Chmod(0o500)
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, binaryPath); err != nil {
		return "", err
	}

	return binaryPath, nil
}

func fetchDropbearArtifact(ctx context.Context, artifactURL string, manifestURL string) ([]byte, error) {
	artifact, err := url.Parse(artifactURL)
	if err != nil || artifact.User != nil {
		return nil, fmt.Errorf("invalid Dropbear artifact URL")
	}
	origin, err := url.Parse(manifestURL)
	if err != nil {
		return nil, err
	}
	// The manifest authorizes a checksum, never a request to another origin.
	artifact.Scheme = origin.Scheme
	artifact.Host = origin.Host
	fetchCtx, cancel := context.WithTimeout(ctx, defaultDropbearFetchTimeout)
	defer cancel()
	return fetchBytes(fetchCtx, artifact.String())
}

func dropbearArtifactFallbackURL(artifactURL string, manifestURL string) (string, bool) {
	artifact, err := url.Parse(strings.TrimSpace(artifactURL))
	if err != nil || artifact.Scheme == "" || artifact.Host == "" {
		return "", false
	}
	manifest, err := url.Parse(strings.TrimSpace(manifestURL))
	if err != nil || manifest.Scheme == "" || manifest.Host == "" {
		return "", false
	}
	if strings.EqualFold(artifact.Scheme, manifest.Scheme) && strings.EqualFold(artifact.Host, manifest.Host) {
		return "", false
	}
	artifact.Scheme = manifest.Scheme
	artifact.Host = manifest.Host
	return artifact.String(), true
}

func fetchDropbearManifest(ctx context.Context) (dropbearManifest, error) {
	var manifest dropbearManifest

	manifestURL := strings.TrimSpace(dropbearManifestURL)
	if manifestURL == "" {
		return manifest, fmt.Errorf("dropbear manifest URL is empty")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, defaultDropbearFetchTimeout)
	defer cancel()

	payload, err := fetchBytes(fetchCtx, manifestURL)
	if err != nil {
		return manifest, fmt.Errorf("fetch dropbear manifest: %w", err)
	}

	if err := json.Unmarshal(payload, &manifest); err != nil {
		return manifest, fmt.Errorf("decode dropbear manifest: %w", err)
	}
	if len(manifest.Binaries) == 0 {
		return manifest, fmt.Errorf("dropbear manifest has no binaries")
	}

	return manifest, nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "swarmind-connect/dropbear-autofetch")

	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || (len(via) > 0 && (next.URL.Scheme != via[0].URL.Scheme || next.URL.Host != via[0].URL.Host)) {
			return fmt.Errorf("unsafe Dropbear redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxDropbearFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(payload) > maxDropbearFetchBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxDropbearFetchBytes)
	}
	return payload, nil
}

func validateDropbearSHA256(payload []byte, want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return fmt.Errorf("dropbear sha256 is required in manifest")
	}
	want = strings.ToLower(want)

	hash := sha256.Sum256(payload)
	got := hex.EncodeToString(hash[:])
	if got != want {
		return fmt.Errorf("dropbear sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}

func (m dropbearManifest) match(goos string, goarch string) (dropbearManifestBinary, bool) {
	for _, item := range m.Binaries {
		if strings.EqualFold(strings.TrimSpace(item.OS), goos) &&
			strings.EqualFold(strings.TrimSpace(item.Arch), goarch) &&
			strings.TrimSpace(item.URL) != "" {
			return item, true
		}
	}
	return dropbearManifestBinary{}, false
}

func buildDropbearStartScript(containerBinaryPath string) string {
	lines := []string{
		"set -eu",
		"mkdir -p /root/.ssh /etc/dropbear /var/run /tmp/.antgpu",
		"chmod 700 /root/.ssh",
	}

	if strings.TrimSpace(containerBinaryPath) == "" {
		lines = append(lines,
			"DROPBEAR_BIN=\"$(command -v dropbear 2>/dev/null || true)\"",
			"if [ -z \"$DROPBEAR_BIN\" ]; then DROPBEAR_BIN=\"/usr/sbin/dropbear\"; fi",
			"if [ ! -x \"$DROPBEAR_BIN\" ]; then exit 44; fi",
		)
	} else {
		lines = append(lines,
			fmt.Sprintf("DROPBEAR_BIN=%q", containerBinaryPath),
			"if [ ! -x \"$DROPBEAR_BIN\" ]; then exit 44; fi",
		)
	}

	lines = append(lines,
		"if [ -f /var/run/dropbear.pid ]; then kill \"$(cat /var/run/dropbear.pid)\" >/dev/null 2>&1 || true; fi",
		"if command -v pkill >/dev/null 2>&1; then pkill -x dropbear >/dev/null 2>&1 || true; fi",
		": >/tmp/.antgpu/dropbear.log",
		"\"$DROPBEAR_BIN\" -R -E -p 22 -P /var/run/dropbear.pid -s -g >/tmp/.antgpu/dropbear.log 2>&1 &",
		"i=0",
		"while [ \"$i\" -lt 20 ]; do",
		"  if [ -f /var/run/dropbear.pid ] && kill -0 \"$(cat /var/run/dropbear.pid)\" >/dev/null 2>&1; then",
		"    if awk '($2 ~ /:0016$/ && $4 == \"0A\") {found=1} END {exit(found?0:1)}' /proc/net/tcp /proc/net/tcp6 2>/dev/null; then",
		"      exit 0",
		"    fi",
		"  fi",
		"  i=$((i+1))",
		"  sleep 0.1",
		"done",
		"echo \"dropbear failed to listen on :22\"",
		"if [ -f /tmp/.antgpu/dropbear.log ]; then tail -n 50 /tmp/.antgpu/dropbear.log; fi",
		fmt.Sprintf("exit %d", dropbearStartFailedExitCode),
	)

	return strings.Join(lines, "\n")
}

func finalizeContainerSSHKeys(ctx context.Context, cli *client.Client, containerID string, runtimeKind sshRuntime) error {
	reloadScript := []string{
		"set -eu",
		"mkdir -p /root/.ssh",
		"chmod 700 /root/.ssh",
		"chmod 600 /root/.ssh/authorized_keys",
	}

	if runtimeKind == sshRuntimeOpenSSH {
		reloadScript = append(reloadScript,
			"if command -v pkill >/dev/null 2>&1; then pkill -HUP sshd >/dev/null 2>&1 || true; fi",
		)
	}
	if runtimeKind == sshRuntimeDropbear {
		reloadScript = append(reloadScript,
			"if [ -f /var/run/dropbear.pid ] && ! kill -0 \"$(cat /var/run/dropbear.pid)\" >/dev/null 2>&1; then rm -f /var/run/dropbear.pid; fi",
		)
	}

	exitCode, output, err := execInContainer(ctx, cli, containerID, []string{"/bin/sh", "-lc", strings.Join(reloadScript, "\n")})
	if err != nil {
		return fmt.Errorf("reload ssh daemon: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("reload ssh daemon failed with exit code %d: %s", exitCode, compactExecOutput(output))
	}

	return nil
}

func compactExecOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "no output"
	}
	if len(trimmed) > 240 {
		return trimmed[:240] + "..."
	}
	return trimmed
}

func normalizeContainerSSHPublicKeys(publicKeys []string) []string {
	out := make([]string, 0, len(publicKeys))
	for _, item := range publicKeys {
		key := strings.TrimSpace(item)
		if key == "" || strings.ContainsAny(key, "\r\n") {
			continue
		}
		if slices.Contains(out, key) {
			continue
		}
		out = append(out, key)
	}
	return out
}

func execInContainer(ctx context.Context, cli *client.Client, containerID string, cmd []string) (int, string, error) {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		User:         "root",
	})
	if err != nil {
		return -1, "", err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, "", err
	}
	defer hijacked.Close()

	output, _ := io.ReadAll(hijacked.Reader)

	for {
		inspect, inspectErr := cli.ContainerExecInspect(ctx, execResp.ID)
		if inspectErr != nil {
			return -1, string(output), inspectErr
		}
		if !inspect.Running {
			return inspect.ExitCode, string(output), nil
		}

		select {
		case <-ctx.Done():
			return -1, string(output), ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func copyFileToContainer(ctx context.Context, cli *client.Client, containerID, filePath string, content []byte, mode int64) error {
	dir := path.Dir(filePath)
	name := path.Base(filePath)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(content)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, dir, &buf, container.CopyToContainerOptions{
		CopyUIDGID: false,
	})
}
