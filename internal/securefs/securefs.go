// Package securefs protects files that the privileged connector writes or executes.
package securefs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidateVersion(version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid artifact version %q", version)
	}
	return nil
}

// PrivateDir refuses symlinks and untrusted writable ancestors before creating
// anything. Existing directories must belong to this process's user.
func PrivateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := trustedAncestors(abs); err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	if err := trustedAncestors(abs); err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe directory %s", abs)
	}
	if err := checkOwner(info, true); err != nil {
		return fmt.Errorf("unsafe directory %s: %w", abs, err)
	}
	return os.Chmod(abs, 0o700)
}

func trustedAncestors(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("unsafe directory ancestor %s", p)
			}
			if err := checkOwner(info, false); err != nil {
				return fmt.Errorf("untrusted directory %s: %w", p, err)
			}
			if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("writable directory ancestor %s", p)
			}
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func RegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe file %s", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("writable executable %s", path)
	}
	if err := checkOwner(info, true); err != nil {
		return fmt.Errorf("untrusted file %s: %w", path, err)
	}
	return trustedAncestors(filepath.Dir(path))
}

// RemoveOwnedScript only removes a regular direct child of the private script
// directory. Labels from old containers never authorize deleting arbitrary paths.
func RemoveOwnedScript(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Dir(absPath) != absRoot || !strings.HasSuffix(filepath.Base(absPath), ".sh") {
		return fmt.Errorf("script path is outside connector directory")
	}
	if err := PrivateDir(absRoot); err != nil {
		return err
	}
	if err := RegularFile(absPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Remove(absPath)
}
