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
