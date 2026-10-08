//go:build !windows

package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func privateTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPrivateDirRejectsSymlinksAndWritableAncestors(t *testing.T) {
	root := privateTestRoot(t)
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(filepath.Join(link, "cache")); err == nil {
		t.Fatal("followed symlink")
	}
	writable := filepath.Join(root, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(filepath.Join(writable, "cache")); err == nil {
		t.Fatal("accepted world-writable ancestor")
	}
	safe := filepath.Join(root, "safe", "cache")
	if err := PrivateDir(safe); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(safe)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache mode: %v %v", info, err)
	}
}

func TestRemoveOwnedScriptRejectsOutsidePathsAndSymlinks(t *testing.T) {
	root := privateTestRoot(t)
	scripts := filepath.Join(root, "scripts")
	if err := PrivateDir(scripts); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim.sh")
	if err := os.WriteFile(victim, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedScript(scripts, victim); err == nil {
		t.Fatal("accepted external cleanup path")
	}
	link := filepath.Join(scripts, "malicious.sh")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedScript(scripts, link); err == nil {
		t.Fatal("accepted symlink cleanup")
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "preserve" {
		t.Fatalf("victim changed: %q %v", data, err)
	}
	owned := filepath.Join(scripts, "bootstrap-owned.sh")
	if err := os.WriteFile(owned, []byte("owned"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedScript(scripts, owned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned script not removed: %v", err)
	}
}

func TestValidateVersionRejectsTraversal(t *testing.T) {
	for _, version := range []string{"", ".", "..", "../victim", "v1/../../etc", "/root", "v1\nnext", "v1\\etc"} {
		if ValidateVersion(version) == nil {
			t.Errorf("accepted %q", version)
		}
	}
	if err := ValidateVersion("2026.10.08-rc1"); err != nil {
		t.Fatal(err)
	}
}
