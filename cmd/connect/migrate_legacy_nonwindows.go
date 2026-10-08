//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/antgpu/connector/internal/securefs"
)

func isConnectorBuild(info *debug.BuildInfo) bool {
	if info == nil {
		return false
	}
	return (info.Main.Path == "github.com/antgpu/connect" && info.Path == "github.com/antgpu/connect/cmd/connect") ||
		(info.Main.Path == "github.com/antgpu/connector" && info.Path == "github.com/antgpu/connector/cmd/connect")
}

func isLegacyCachedExecutable(executable, tempDir, version, arch string) bool {
	return executable == filepath.Join(tempDir, ".antgpu", "connect-cache", version, "linux-"+arch, "connect")
}

// The legacy cache was shared and its name can be changed after exec. Snapshot
// the actual running inode through /proc/self/exe into our private state, then
// authenticate that immutable-to-other-users copy before changing the target.
func persistLegacyCandidate(running io.Reader, stagingDir, target string, verify func(string) error) error {
	if err := securefs.PrivateDir(stagingDir); err != nil {
		return err
	}
	f, err := os.CreateTemp(stagingDir, "migration-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.Copy(f, running); err != nil {
		return err
	}
	if err := f.Chmod(0o500); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := verify(f.Name()); err != nil {
		return fmt.Errorf("authenticate running candidate: %w", err)
	}
	return installReplacementBinary(f.Name(), target)
}

var literalExecutable = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
var shellExec = regexp.MustCompile(`(?:^|[;\s])exec\s+(\S+)`)

// Inspect systemd's structured argv; never execute/evaluate a shell wrapper.
// Released installers used literal exec paths, sometimes twice in auth branches.
// Every exec must name the same literal path. Arbitrary wrappers fail closed.
func serviceExecutable(path string, args []string) (string, error) {
	if path != "/bin/sh" && path != "/usr/bin/sh" && path != "/bin/bash" && path != "/usr/bin/bash" {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", fmt.Errorf("service executable is not an absolute clean path")
		}
		return path, nil
	}
	if len(args) != 3 || (args[1] != "-c" && args[1] != "-lc") {
		return "", fmt.Errorf("unsupported connector service wrapper")
	}
	matches := shellExec.FindAllStringSubmatch(args[2], -1)
	target := ""
	for _, match := range matches {
		p := match[1]
		if !literalExecutable.MatchString(p) || filepath.Clean(p) != p || (target != "" && target != p) {
			return "", fmt.Errorf("ambiguous or nonliteral service executable; rerun the state-preserving installer")
		}
		target = p
	}
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("no literal connector exec in service wrapper")
	}
	return target, nil
}
