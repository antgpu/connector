package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/antgpu/connector-protocol"
)

func TestVerifyRunningCandidateRejectsInvalidBootstrap(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "running-snapshot")
	if err := os.WriteFile(path, []byte("running binary"), 0o500); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("running binary"))
	valid := protocol.UpdateManifest{Version: "v1", Binaries: []protocol.UpdateArtifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "https://api.example/connect", SHA256: hex.EncodeToString(sum[:])}}}
	for _, kind := range []string{"valid", "unsigned", "tampered-signature", "wrong-version", "wrong-platform", "wrong-bytes"} {
		t.Run(kind, func(t *testing.T) {
			m := valid
			m.Binaries = append([]protocol.UpdateArtifact(nil), valid.Binaries...)
			if kind == "wrong-version" {
				m.Version = "v2"
			}
			if kind == "wrong-platform" {
				m.Binaries[0].Arch = "unsupported"
			}
			if kind == "wrong-bytes" {
				m.Binaries[0].SHA256 = strings.Repeat("a", 64)
			}
			if kind != "unsigned" {
				if err := protocol.SignUpdateManifest(&m, base64.StdEncoding.EncodeToString(key)); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "tampered-signature" {
				m.Version = "forged"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(m) }))
			defer server.Close()
			manager := NewManager(ManagerConfig{APIAddr: server.URL, MachineToken: "test-only", Version: "v1", PublicKey: base64.StdEncoding.EncodeToString(pub)})
			err := manager.VerifyRunningCandidate(context.Background(), path)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestBinaryFilename(t *testing.T) {
	if got := binaryFilename("windows"); got != "connect.exe" {
		t.Fatalf("binaryFilename(windows)=%q want %q", got, "connect.exe")
	}
	if got := binaryFilename("linux"); got != "connect" {
		t.Fatalf("binaryFilename(linux)=%q want %q", got, "connect")
	}
}

func TestDownloadArtifactDoesNotFollowExistingSymlink(t *testing.T) {
	payload := []byte("replacement")
	sum := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	link := filepath.Join(root, "artifact.tmp")
	if err := os.WriteFile(victim, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := downloadArtifact(context.Background(), artifactHTTPClient, server.URL, link, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("accepted preexisting symlink")
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "preserve" {
		t.Fatalf("victim overwritten: %q %v", data, err)
	}
}

func TestFetchManifestRequiresTrustedSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := protocol.UpdateManifest{Version: "2026.10.08", Binaries: []protocol.UpdateArtifact{{OS: "linux", Arch: "amd64", URL: "https://api.example/connect", SHA256: strings.Repeat("a", 64)}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(m) }))
	defer server.Close()
	manager := NewManager(ManagerConfig{APIAddr: server.URL, MachineToken: "test-only", PublicKey: base64.StdEncoding.EncodeToString(pub)})
	if _, err := manager.fetchManifest(context.Background()); err == nil {
		t.Fatal("accepted unsigned manifest")
	}
	if err := protocol.SignUpdateManifest(&m, base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.fetchManifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Version = "2026.10.09"
	if _, err := manager.fetchManifest(context.Background()); err == nil {
		t.Fatal("accepted tampered manifest")
	}
}

func TestArtifactRedirectRejectsCrossOriginAndDowngrade(t *testing.T) {
	origin, _ := http.NewRequest(http.MethodGet, "https://api.example/file", nil)
	for _, target := range []string{"https://evil.example/file", "http://api.example/file"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if sameOriginRedirect(req, []*http.Request{origin}) == nil {
			t.Errorf("accepted redirect to %s", target)
		}
	}
}

func TestCachedBinaryPath_Windows(t *testing.T) {
	got := cachedBinaryPath("C:/cache", "2026.05.24.1", "windows", "amd64")
	want := filepath.Join("C:/cache", "2026.05.24.1", "windows-amd64", "connect.exe")
	if got != want {
		t.Fatalf("cachedBinaryPath windows=%q want %q", got, want)
	}
}

func TestCachedBinaryPath_Linux(t *testing.T) {
	got := cachedBinaryPath("/tmp/cache", "2026.05.24.1", "linux", "amd64")
	want := filepath.Join("/tmp/cache", "2026.05.24.1", "linux-amd64", "connect")
	if got != want {
		t.Fatalf("cachedBinaryPath linux=%q want %q", got, want)
	}
}

func TestDownloadArtifactForcesHTTP1(t *testing.T) {
	payload := strings.Repeat("connector-binary", 1024)
	sum := sha256.Sum256([]byte(payload))
	protocol := ""

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol = r.Proto
		_, _ = w.Write([]byte(payload))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Test server certificate.
	client := &http.Client{Transport: newArtifactHTTPTransport(base)}
	outPath := filepath.Join(t.TempDir(), "connect.exe.tmp")

	if err := downloadArtifact(context.Background(), client, server.URL, outPath, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("download artifact: %v", err)
	}
	if protocol != "HTTP/1.1" {
		t.Fatalf("artifact request protocol=%q want HTTP/1.1", protocol)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read downloaded artifact: %v", err)
	}
	if string(got) != payload {
		t.Fatal("downloaded artifact content mismatch")
	}
}

func TestDownloadArtifactRemovesInvalidFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unexpected"))
	}))
	defer server.Close()

	outPath := filepath.Join(t.TempDir(), "connect.tmp")
	err := downloadArtifact(context.Background(), artifactHTTPClient, server.URL, outPath, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch, got %v", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("invalid artifact was not removed: %v", statErr)
	}
}

type deadlineTransport struct {
	base      http.RoundTripper
	remaining chan time.Duration
}

func (d deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	end, _ := r.Context().Deadline()
	d.remaining <- time.Until(end)
	return d.base.RoundTrip(r)
}

func TestUpdateSerializesDownloadAndRechecksRent(t *testing.T) {
	for _, rentStarts := range []bool{false, true} {
		t.Run(fmt.Sprint("rent-starts-", rentStarts), func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			payload := []byte("signed test artifact")
			sum := sha256.Sum256(payload)
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var downloads atomic.Int32
			var manifest protocol.UpdateManifest
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/manifest") {
					_ = json.NewEncoder(w).Encode(manifest)
					return
				}
				downloads.Add(1)
				once.Do(func() { close(started) })
				<-release
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			manifest = protocol.UpdateManifest{Version: "next", Binaries: []protocol.UpdateArtifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: server.URL + "/binary", SHA256: hex.EncodeToString(sum[:])}}}
			if err := protocol.SignUpdateManifest(&manifest, base64.StdEncoding.EncodeToString(key)); err != nil {
				t.Fatal(err)
			}
			oldDefault, oldArtifact := http.DefaultClient, artifactHTTPClient
			defer func() { http.DefaultClient = oldDefault; artifactHTTPClient = oldArtifact }()
			http.DefaultClient = server.Client()
			remaining := make(chan time.Duration, 1)
			artifactHTTPClient = &http.Client{Transport: deadlineTransport{base: server.Client().Transport, remaining: remaining}}
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			applied := make(chan string, 1)
			var rentChecks atomic.Int32
			m := NewManager(ManagerConfig{APIAddr: server.URL, MachineToken: "test-only", Version: "old", PublicKey: base64.StdEncoding.EncodeToString(pub), CacheRoot: filepath.Join(root, "cache"), HasRunningManagedContainers: func(context.Context) (bool, error) { return rentChecks.Add(1) > 1 && rentStarts, nil }, RestartBinary: func(p string) error { applied <- p; return nil }})
			type outcome struct {
				result Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := m.RequestUpdate(context.Background(), Request{}); done <- outcome{result, err} }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("download did not start")
			}
			duplicate, err := m.RequestUpdate(context.Background(), Request{})
			if err != nil || duplicate.Reason != "already_updating" {
				close(release)
				t.Fatalf("concurrent request: %v %v", duplicate, err)
			}
			if left := <-remaining; left < 4*time.Minute {
				close(release)
				t.Fatalf("artifact deadline too short: %s", left)
			}
			close(release)
			result := <-done
			if result.err != nil {
				t.Fatal(result.err)
			}
			if downloads.Load() != 1 {
				t.Fatalf("duplicate downloads: %d", downloads.Load())
			}
			if rentStarts {
				if !result.result.Deferred || result.result.Reason != "active_rent" {
					t.Fatalf("rent was interrupted: %+v", result.result)
				}
				select {
				case <-applied:
					t.Fatal("restarted during rent")
				default:
				}
			} else {
				select {
				case <-applied:
				case <-time.After(5 * time.Second):
					t.Fatal("update did not apply")
				}
			}
		})
	}
}
