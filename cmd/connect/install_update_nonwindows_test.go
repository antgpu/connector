//go:build !windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallReplacementBinaryRetainsOldInodeAndRecoveryCopy(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(dir, "candidate"), filepath.Join(dir, "connect")
	if err := os.WriteFile(source, []byte("new"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	if err := installReplacementBinary(source, target); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, want string }{{source, "new"}, {target, "new"}, {target + ".previous", "old"}} {
		data, err := os.ReadFile(item.path)
		if err != nil || string(data) != item.want {
			t.Fatalf("%s: %q, %v", item.path, data, err)
		}
	}
	data, err := io.ReadAll(running)
	if err != nil || string(data) != "old" {
		t.Fatalf("running inode changed: %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode changed: %v, %v", info, err)
	}
}

func TestInstallReplacementBinaryRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"source-symlink", "target-symlink", "previous-symlink", "writable-target", "nonexecutable"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(dir, "candidate"), filepath.Join(dir, "connect")
			os.WriteFile(source, []byte("new"), 0o500)
			os.WriteFile(target, []byte("old"), 0o755)
			switch kind {
			case "source-symlink":
				os.Remove(source)
				os.Symlink(target, source)
			case "target-symlink":
				os.Remove(target)
				os.Symlink(source, target)
			case "previous-symlink":
				os.Symlink(source, target+".previous")
			case "writable-target":
				os.Chmod(target, 0o777)
			case "nonexecutable":
				os.Chmod(target, 0o600)
			}
			if err := installReplacementBinary(source, target); err == nil {
				t.Fatal("unsafe replacement was accepted")
			}
		})
	}
}
