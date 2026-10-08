package updater

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	protocol "github.com/antgpu/connector-protocol"
	"github.com/antgpu/connector/internal/securefs"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultFetchTimeout = 30 * time.Second
	maxFetchBytes       = 256 * 1024 * 1024
)

var artifactHTTPClient = &http.Client{
	Transport:     newArtifactHTTPTransport(http.DefaultTransport.(*http.Transport)),
	CheckRedirect: sameOriginRedirect,
}

func newArtifactHTTPTransport(base *http.Transport) *http.Transport {
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.ForceAttemptHTTP2 = false
	transport.DisableKeepAlives = true
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return transport
}

type State string

const (
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected"
	StateUpdating     State = "updating"
)

type Snapshot struct {
	Version       string    `json:"version"`
	State         string    `json:"state"`
	TargetVersion string    `json:"target_version,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Result struct {
	Accepted      bool   `json:"accepted"`
	AlreadyLatest bool   `json:"already_latest"`
	Deferred      bool   `json:"deferred"`
	Reason        string `json:"reason,omitempty"`
	Version       string `json:"version,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	State         string `json:"state,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type Request struct {
	Version string `json:"version,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

type ManagerConfig struct {
	APIAddr      string
	MachineToken string
	Version      string
	CacheRoot    string
	PublicKey    string

	HasRunningManagedContainers func(ctx context.Context) (bool, error)
	RestartBinary               func(binaryPath string) error
	Logger                      func(format string, args ...any)
}

type Manager struct {
	apiAddr      string
	machineToken string
	cacheRoot    string
	publicKey    string
	logger       func(format string, args ...any)

	hasRunningManagedContainers func(ctx context.Context) (bool, error)
	restartBinary               func(binaryPath string) error

	mu       sync.Mutex
	updating bool
	snapshot Snapshot
}

type manifest = protocol.UpdateManifest
type manifestItem = protocol.UpdateArtifact

func NewManager(cfg ManagerConfig) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = func(string, ...any) {}
	}

	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		version = "dev"
	}

	cacheRoot := strings.TrimSpace(cfg.CacheRoot)
	if cacheRoot == "" {
		cacheRoot = filepath.Join("cache", "updates")
	}

	now := time.Now().UTC()
	return &Manager{
		apiAddr:                     strings.TrimRight(strings.TrimSpace(cfg.APIAddr), "/"),
		machineToken:                strings.TrimSpace(cfg.MachineToken),
		cacheRoot:                   cacheRoot,
		publicKey:                   cfg.PublicKey,
		logger:                      logger,
		hasRunningManagedContainers: cfg.HasRunningManagedContainers,
		restartBinary:               cfg.RestartBinary,
		snapshot: Snapshot{
			Version:   version,
			State:     string(StateDisconnected),
			UpdatedAt: now,
		},
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot
}

func (m *Manager) SetConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot.State = string(StateConnected)
	m.snapshot.UpdatedAt = time.Now().UTC()
}

func (m *Manager) SetDisconnected(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot.State = string(StateDisconnected)
	if strings.TrimSpace(reason) != "" {
		m.snapshot.LastError = strings.TrimSpace(reason)
	}
	m.snapshot.UpdatedAt = time.Now().UTC()
}

func (m *Manager) Poll(ctx context.Context) (Result, error) {
	return m.RequestUpdate(ctx, Request{})
}

// VerifyRunningCandidate authenticates a private snapshot of the executable
// launched by a legacy updater before it can replace the service executable.
// Legacy clients themselves do not verify manifest signatures.
func (m *Manager) VerifyRunningCandidate(ctx context.Context, path string) error {
	man, err := m.fetchManifest(ctx)
	if err != nil {
		return err
	}
	if man.Version != m.Snapshot().Version {
		return fmt.Errorf("running version does not match signed active manifest")
	}
	item, ok := pickArtifact(man, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("signed manifest has no artifact for this platform")
	}
	return verifyCachedArtifact(path, item.SHA256)
}

func (m *Manager) RequestUpdate(ctx context.Context, req Request) (Result, error) {
	m.mu.Lock()
	if m.updating {
		out := Result{
			Accepted:      false,
			AlreadyLatest: false,
			Deferred:      false,
			Reason:        "already_updating",
			Version:       m.snapshot.Version,
			TargetVersion: m.snapshot.TargetVersion,
			State:         m.snapshot.State,
			LastError:     m.snapshot.LastError,
		}
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()

	man, err := m.fetchManifest(ctx)
	if err != nil {
		m.setLastError(err)
		return Result{}, err
	}

	target := strings.TrimSpace(req.Version)
	if target == "" {
		target = strings.TrimSpace(man.Version)
	}
	if target != man.Version {
		return Result{}, fmt.Errorf("requested version does not match signed active manifest")
	}
	if err := securefs.ValidateVersion(target); err != nil {
		return Result{}, err
	}

	item, ok := pickArtifact(man, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return Result{Deferred: true, Reason: "binary_not_found_for_platform", Version: m.Snapshot().Version, TargetVersion: target, State: m.Snapshot().State}, nil
	}

	snap := m.Snapshot()
	if !req.Force && strings.TrimSpace(snap.Version) == target {
		return Result{
			Accepted:      false,
			AlreadyLatest: true,
			Version:       snap.Version,
			TargetVersion: target,
			State:         snap.State,
		}, nil
	}

	if m.hasRunningManagedContainers != nil {
		running, runErr := m.hasRunningManagedContainers(ctx)
		if runErr != nil {
			m.setLastError(runErr)
			return Result{}, runErr
		}
		if running {
			m.mu.Lock()
			m.snapshot.TargetVersion = target
			m.snapshot.UpdatedAt = time.Now().UTC()
			m.mu.Unlock()
			return Result{
				Deferred:      true,
				Reason:        "active_rent",
				Version:       snap.Version,
				TargetVersion: target,
				State:         snap.State,
			}, nil
		}
	}

	binaryPath, err := m.downloadAndCache(ctx, target, item)
	if err != nil {
		m.setLastError(err)
		return Result{}, err
	}

	m.mu.Lock()
	m.updating = true
	m.snapshot.State = string(StateUpdating)
	m.snapshot.TargetVersion = target
	m.snapshot.LastError = ""
	m.snapshot.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	go func() {
		if err := verifyCachedArtifact(binaryPath, item.SHA256); err != nil {
			m.setUpdateFailed(err)
			return
		}
		m.applyUpdate(binaryPath, target)
	}()

	return Result{
		Accepted:      true,
		Version:       snap.Version,
		TargetVersion: target,
		State:         string(StateUpdating),
	}, nil
}

func (m *Manager) applyUpdate(binaryPath, target string) {
	if m.restartBinary == nil {
		m.setUpdateFailed(fmt.Errorf("restart callback is not configured"))
		return
	}

	if err := m.restartBinary(binaryPath); err != nil {
		m.setUpdateFailed(err)
		return
	}

	m.logger("connector updater: restart requested for target=%s binary=%s", target, binaryPath)
}

func (m *Manager) setUpdateFailed(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updating = false
	m.snapshot.State = string(StateConnected)
	m.snapshot.LastError = strings.TrimSpace(err.Error())
	m.snapshot.UpdatedAt = time.Now().UTC()
}

func (m *Manager) setLastError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot.LastError = strings.TrimSpace(err.Error())
	m.snapshot.UpdatedAt = time.Now().UTC()
}

func (m *Manager) fetchManifest(ctx context.Context) (manifest, error) {
	var out manifest
	if strings.TrimSpace(m.apiAddr) == "" {
		return out, fmt.Errorf("api addr is empty")
	}
	if strings.TrimSpace(m.machineToken) == "" {
		return out, fmt.Errorf("machine token is empty")
	}

	reqCtx, cancel := context.WithTimeout(ctx, defaultFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, m.apiAddr+"/machines/connector/manifest", nil)
	if err != nil {
		return out, fmt.Errorf("create manifest request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.machineToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("send manifest request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("manifest request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return out, fmt.Errorf("read manifest response: %w", err)
	}
	if len(payload) > 1<<20 {
		return out, fmt.Errorf("connector manifest exceeds size limit")
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return out, fmt.Errorf("decode manifest response: %w", err)
	}
	if strings.TrimSpace(out.Version) == "" {
		return out, fmt.Errorf("manifest version is empty")
	}
	if len(out.Binaries) == 0 {
		return out, fmt.Errorf("manifest has no binaries")
	}

	if err := protocol.VerifyUpdateManifest(out, m.publicKey); err != nil {
		return out, fmt.Errorf("verify connector manifest: %w", err)
	}
	return out, nil
}

func (m *Manager) downloadAndCache(ctx context.Context, version string, item manifestItem) (string, error) {
	if err := securefs.ValidateVersion(version); err != nil {
		return "", err
	}
	base, err := url.Parse(m.apiAddr)
	if err != nil {
		return "", err
	}
	artifact, err := url.Parse(item.URL)
	if err != nil || artifact.Scheme != "https" || artifact.Host != base.Host || artifact.User != nil {
		return "", fmt.Errorf("update artifact must use HTTPS on the configured API host")
	}
	outPath := cachedBinaryPath(m.cacheRoot, version, runtime.GOOS, runtime.GOARCH)
	if err := securefs.PrivateDir(filepath.Dir(outPath)); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(outPath), "artifact-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := temp.Name()
	if err := temp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Remove(tmpPath); err != nil {
		return "", err
	}
	defer os.Remove(tmpPath)
	reqCtx, cancel := context.WithTimeout(ctx, defaultFetchTimeout)
	defer cancel()
	if err := downloadArtifact(reqCtx, artifactHTTPClient, item.URL, tmpPath, item.SHA256); err != nil {
		return "", fmt.Errorf("download binary: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("commit binary cache: %w", err)
	}
	if err := os.Chmod(outPath, 0o500); err != nil {
		return "", fmt.Errorf("chmod binary: %w", err)
	}

	return outPath, nil
}

func cachedBinaryPath(cacheRoot, version, goos, goarch string) string {
	platform := strings.TrimSpace(goos) + "-" + strings.TrimSpace(goarch)
	return filepath.Join(cacheRoot, strings.TrimSpace(version), platform, binaryFilename(goos))
}

func binaryFilename(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "connect.exe"
	}
	return "connect"
}

func pickArtifact(man manifest, goos, goarch string) (manifestItem, bool) {
	for _, item := range man.Binaries {
		if !strings.EqualFold(strings.TrimSpace(item.OS), strings.TrimSpace(goos)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.Arch), strings.TrimSpace(goarch)) {
			continue
		}
		if strings.TrimSpace(item.URL) == "" || strings.TrimSpace(item.SHA256) == "" {
			continue
		}
		return item, true
	}

	return manifestItem{}, false
}

func downloadArtifact(ctx context.Context, client *http.Client, url, outPath, expectedSHA256 string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(url), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Close = true

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("unexpected status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.ContentLength > maxFetchBytes {
		return fmt.Errorf("response body exceeds %d bytes", maxFetchBytes)
	}

	file, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temp binary: %w", err)
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		_ = file.Close()
		_ = os.Remove(outPath)
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return fmt.Errorf("read response body after %d bytes: %w", written, err)
	}
	if written == 0 {
		return fmt.Errorf("downloaded binary is empty")
	}
	if written > maxFetchBytes {
		return fmt.Errorf("response body exceeds %d bytes", maxFetchBytes)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("response body length mismatch: expected=%d actual=%d", resp.ContentLength, written)
	}
	if err := verifySHA256Hex(hex.EncodeToString(hasher.Sum(nil)), expectedSHA256); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chmod(0o500); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp binary: %w", err)
	}
	completed = true
	return nil
}

func verifySHA256Hex(actual, expectedHex string) error {
	expected := strings.ToLower(strings.TrimSpace(expectedHex))
	if expected == "" {
		return fmt.Errorf("sha256 is required")
	}

	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("sha256 mismatch: expected=%s actual=%s", expected, actual)
	}

	return nil
}

func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many artifact redirects")
	}
	if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host) {
		return fmt.Errorf("cross-origin artifact redirect refused")
	}
	return nil
}

func verifyCachedArtifact(path, expected string) error {
	if err := securefs.RegularFile(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	return verifySHA256Hex(hex.EncodeToString(hash.Sum(nil)), expected)
}
