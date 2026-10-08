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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/antgpu/connector-protocol"
)

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
	want := "C:/cache/2026.05.24.1/windows-amd64/connect.exe"
	if got != want {
		t.Fatalf("cachedBinaryPath windows=%q want %q", got, want)
	}
}

func TestCachedBinaryPath_Linux(t *testing.T) {
	got := cachedBinaryPath("/tmp/cache", "2026.05.24.1", "linux", "amd64")
	want := "/tmp/cache/2026.05.24.1/linux-amd64/connect"
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
