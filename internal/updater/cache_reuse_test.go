package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/antgpu/connector/internal/securefs"
)

func cachedRollbackFixture(t *testing.T) (*Manager, string, manifestItem) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{APIAddr: "https://127.0.0.1:1", CacheRoot: filepath.Join(root, "cache")})
	path := cachedBinaryPath(m.cacheRoot, "rollback-test", runtime.GOOS, runtime.GOARCH)
	if err := securefs.PrivateDir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("trusted cached executable fixture")
	if err := os.WriteFile(path, payload, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := securefs.ExecutableFile(path); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return m, path, manifestItem{URL: "https://127.0.0.1:1/binary", SHA256: hex.EncodeToString(sum[:])}
}

func TestDownloadAndCacheReusesVerifiedArtifact(t *testing.T) {
	m, path, item := cachedRollbackFixture(t)
	got, err := m.downloadAndCache(context.Background(), "rollback-test", item)
	if err != nil || got != path {
		t.Fatalf("could not reuse protected cached artifact without a download: %q %v", got, err)
	}
}

func TestDownloadAndCacheRejectsCacheHashMismatch(t *testing.T) {
	m, path, item := cachedRollbackFixture(t)
	item.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	got, err := m.downloadAndCache(context.Background(), "rollback-test", item)
	if err == nil || got != "" {
		t.Fatal("reused cached bytes with an untrusted hash")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "trusted cached executable fixture" {
		t.Fatalf("failed download changed the cache: %q %v", data, err)
	}
}
