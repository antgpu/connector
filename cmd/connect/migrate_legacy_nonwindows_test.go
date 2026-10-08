//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceExecutableLegacyLayouts(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		args       []string
		want       string
	}{
		{"direct custom service", "/usr/local/bin/antgpu-connect-dev", nil, "/usr/local/bin/antgpu-connect-dev"},
		{"installer wrapper", "/bin/sh", []string{"/bin/sh", "-c", `exec /usr/local/bin/connect --foreground "$ANTGPU_CONNECT_TOKEN"`}, "/usr/local/bin/connect"},
		{"old enrollment wrapper", "/bin/sh", []string{"/bin/sh", "-lc", `if [ -f .auth ]; then exec /usr/local/bin/connect --foreground; elif [ -n "${ANTGPU_CONNECT_TOKEN:-}" ]; then exec /usr/local/bin/connect --foreground "$ANTGPU_CONNECT_TOKEN"; else echo "missing .auth and ANTGPU_CONNECT_TOKEN"; exit 1; fi`}, "/usr/local/bin/connect"},
		{"different executables", "/bin/sh", []string{"sh", "-c", `if true; then exec /first; else exec /second; fi`}, ""},
		{"variable executable", "/bin/sh", []string{"sh", "-c", `exec $CONNECTOR --foreground`}, ""},
		{"shell substitution", "/bin/sh", []string{"sh", "-c", `exec /tmp/$(touch /marker) --foreground`}, ""},
		{"arbitrary wrapper", "/bin/sh", []string{"sh", "-c", `./wrapper`}, ""},
		{"parent traversal", "/bin/sh", []string{"sh", "-c", `exec /tmp/../bin/connect`}, ""},
		{"relative direct", "connect", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serviceExecutable(tc.path, tc.args)
			if (tc.want == "") != (err != nil) || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestPersistLegacyCandidateAuthenticatesBeforeReplacement(t *testing.T) {
	for _, reject := range []bool{false, true} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "installed")
		if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		verified := false
		err = persistLegacyCandidate(bytes.NewBufferString("signed running inode"), filepath.Join(dir, "private-stage"), target, func(p string) error {
			data, err := os.ReadFile(p)
			if err != nil || string(data) != "signed running inode" {
				t.Fatalf("wrong snapshot: %q, %v", data, err)
			}
			old, err := os.ReadFile(target)
			if err != nil || string(old) != "old" {
				t.Fatal("target changed before authentication")
			}
			verified = true
			if reject {
				return errors.New("signature/checksum rejected")
			}
			return nil
		})
		if !verified || (err != nil) != reject {
			t.Fatalf("verified=%v, err=%v", verified, err)
		}
		data, _ := os.ReadFile(target)
		if reject && string(data) != "old" {
			t.Fatal("unauthenticated target replacement")
		}
		if !reject && string(data) != "signed running inode" {
			t.Fatal("migration not persisted")
		}
		entries, _ := os.ReadDir(filepath.Join(dir, "private-stage"))
		if len(entries) != 0 {
			t.Fatal("migration temporary file retained")
		}
	}
}

func TestLegacyCacheDetectionDoesNotAdoptOrdinaryExecutables(t *testing.T) {
	for _, path := range []string{"/usr/local/bin/connect", "/tmp/.antgpu/connect-cache/other/linux-amd64/connect", "/tmp/.antgpu/connect-cache/v1/linux-arm64/connect", "/tmp/other/connect"} {
		if isLegacyCachedExecutable(path, "/tmp", "v1", "amd64") {
			t.Fatalf("adopted unrelated executable %s", path)
		}
	}
	if !isLegacyCachedExecutable("/tmp/.antgpu/connect-cache/v1/linux-amd64/connect", "/tmp", "v1", "amd64") {
		t.Fatal("missed legacy cache")
	}
}
