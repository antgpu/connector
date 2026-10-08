//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/antgpu/connector/internal/securefs"
)

// Install atomically at the service's stable executable path. Merely executing
// the cache would return to the old, potentially unsigned updater on reboot.
func installReplacementBinary(source, target string) error {
	if err := securefs.RegularFile(source); err != nil {
		return fmt.Errorf("validate update source: %w", err)
	}
	if err := securefs.RegularFile(target); err != nil {
		return fmt.Errorf("validate installed executable: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("update target is not executable")
	}
	previous := target + ".previous"
	if _, err := os.Lstat(previous); err == nil {
		if err := securefs.RegularFile(previous); err != nil {
			return fmt.Errorf("validate previous executable: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".connector-update-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Link the old inode without removing the running/service executable. The
	// trusted parent prevents an unprivileged user from racing these names.
	backup, err := os.CreateTemp(filepath.Dir(target), ".connector-previous-")
	if err != nil {
		return err
	}
	backup.Close()
	os.Remove(backup.Name())
	defer os.Remove(backup.Name())
	if err := os.Link(target, backup.Name()); err != nil {
		return err
	}
	if err := os.Rename(backup.Name(), previous); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
