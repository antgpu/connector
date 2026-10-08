package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestAutoFetchDropbearBinary_ManifestCacheAndChecksum(t *testing.T) {
	isolateDropbearCache(t)
	payload := []byte("fake-dropbear-binary")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])
	version := "unit-test-v1"
	expectedPath := filepath.Join(dropbearCacheRoot, version, dropbearTargetOS+"-"+runtime.GOARCH, "dropbear")
	_ = os.RemoveAll(filepath.Dir(expectedPath))

	var manifestHits int32
	var binaryHits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			atomic.AddInt32(&manifestHits, 1)
			fmt.Fprintf(w, `{"version":%q,"binaries":[{"os":%q,"arch":%q,"url":%q,"sha256":%q}]}`,
				version,
				dropbearTargetOS,
				runtime.GOARCH,
				srv.URL+"/dropbear.bin",
				wantSHA,
			)
		case "/dropbear.bin":
			atomic.AddInt32(&binaryHits, 1)
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldManifestURL := dropbearManifestURL
	oldCandidates := hardcodedDropbearBinaryCandidates
	dropbearManifestURL = srv.URL + "/manifest"
	hardcodedDropbearBinaryCandidates = nil
	t.Cleanup(func() {
		dropbearManifestURL = oldManifestURL
		hardcodedDropbearBinaryCandidates = oldCandidates
	})

	path1, err := autoFetchDropbearBinary(context.Background())
	if err != nil {
		t.Fatalf("autoFetchDropbearBinary first call failed: %v", err)
	}
	got1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read fetched binary: %v", err)
	}
	if string(got1) != string(payload) {
		t.Fatalf("unexpected fetched payload: got=%q want=%q", string(got1), string(payload))
	}
	if info, err := os.Stat(path1); err != nil {
		t.Fatalf("stat fetched binary: %v", err)
	} else if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected executable permission on fetched binary, mode=%o", info.Mode().Perm())
	}

	if path1 != expectedPath {
		t.Fatalf("unexpected cache path: got=%q want=%q", path1, expectedPath)
	}

	path2, err := autoFetchDropbearBinary(context.Background())
	if err != nil {
		t.Fatalf("autoFetchDropbearBinary cached call failed: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("expected cached path reuse: first=%q second=%q", path1, path2)
	}
	if got := atomic.LoadInt32(&binaryHits); got != 1 {
		t.Fatalf("expected single binary download, got=%d", got)
	}
	if got := atomic.LoadInt32(&manifestHits); got < 1 {
		t.Fatalf("expected manifest hits, got=%d", got)
	}
	// A cache hit must still verify bytes against the current manifest.
	if err := os.Chmod(path1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path1, []byte("poisoned cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := autoFetchDropbearBinary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&binaryHits); got != 2 {
		t.Fatalf("poisoned cache reused: downloads=%d", got)
	}
	if err := os.Remove(path1); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dropbearCacheRoot, "victim")
	if err := os.WriteFile(victim, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path1); err != nil {
		t.Fatal(err)
	}
	if _, err := autoFetchDropbearBinary(context.Background()); err == nil {
		t.Fatal("accepted symlink cache entry")
	}
}

func TestAutoFetchDropbearBinary_FallsBackToManifestOrigin(t *testing.T) {
	isolateDropbearCache(t)
	payload := []byte("fallback-dropbear-binary")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])
	version := "unit-test-fallback-origin"
	_ = os.RemoveAll(filepath.Join(dropbearCacheRoot, version))

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			fmt.Fprintf(w, `{"version":%q,"binaries":[{"os":%q,"arch":%q,"url":%q,"sha256":%q}]}`,
				version,
				dropbearTargetOS,
				runtime.GOARCH,
				"http://127.0.0.1:1/dropbear.bin",
				wantSHA,
			)
		case "/dropbear.bin":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldManifestURL := dropbearManifestURL
	oldCandidates := hardcodedDropbearBinaryCandidates
	dropbearManifestURL = srv.URL + "/manifest"
	hardcodedDropbearBinaryCandidates = nil
	t.Cleanup(func() {
		dropbearManifestURL = oldManifestURL
		hardcodedDropbearBinaryCandidates = oldCandidates
	})

	path, err := autoFetchDropbearBinary(context.Background())
	if err != nil {
		t.Fatalf("autoFetchDropbearBinary failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fetched binary: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("unexpected fetched payload: got=%q want=%q", string(got), string(payload))
	}
}

func TestAutoFetchDropbearBinary_ChecksumMismatch(t *testing.T) {
	isolateDropbearCache(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			fmt.Fprintf(w, `{"version":"unit-test-bad-sha","binaries":[{"os":%q,"arch":%q,"url":%q,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`,
				dropbearTargetOS,
				runtime.GOARCH,
				srv.URL+"/dropbear.bin",
			)
		case "/dropbear.bin":
			_, _ = w.Write([]byte("binary-data"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldManifestURL := dropbearManifestURL
	oldCandidates := hardcodedDropbearBinaryCandidates
	dropbearManifestURL = srv.URL + "/manifest"
	hardcodedDropbearBinaryCandidates = nil
	t.Cleanup(func() {
		dropbearManifestURL = oldManifestURL
		hardcodedDropbearBinaryCandidates = oldCandidates
	})

	_, err := autoFetchDropbearBinary(context.Background())
	if err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
}

func TestResolveDropbearFallbackBinary_PrefersHardcodedCandidatePath(t *testing.T) {
	tempDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(tempDir, "dropbear-candidate")
	if err := os.WriteFile(candidate, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write candidate binary: %v", err)
	}

	oldManifestURL := dropbearManifestURL
	oldCandidates := hardcodedDropbearBinaryCandidates
	dropbearManifestURL = "http://127.0.0.1:1/manifest"
	hardcodedDropbearBinaryCandidates = []string{candidate}
	t.Cleanup(func() {
		dropbearManifestURL = oldManifestURL
		hardcodedDropbearBinaryCandidates = oldCandidates
	})

	got, err := resolveDropbearFallbackBinary(context.Background())
	if err != nil {
		t.Fatalf("resolveDropbearFallbackBinary failed: %v", err)
	}
	if got != candidate {
		t.Fatalf("expected candidate path, got=%q want=%q", got, candidate)
	}
}

func isolateDropbearCache(t *testing.T) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := dropbearCacheRoot
	dropbearCacheRoot = root
	t.Cleanup(func() { dropbearCacheRoot = old })
}
