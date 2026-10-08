//go:build windows

package main

import (
	"github.com/antgpu/connector/internal/securefs"
	"os"
	"os/exec"
	"path/filepath"
)

func prepareDockerPlatformEnv() error {
	if _, err := exec.LookPath("docker"); err == nil {
		return nil
	}
	programFiles := os.Getenv("ProgramFiles")
	if programFiles == "" {
		programFiles = `C:\Program Files`
	}
	dir := filepath.Join(programFiles, "Docker", "Docker", "resources", "bin")
	if err := securefs.RegularFile(filepath.Join(dir, "docker.exe")); err != nil {
		return err
	}
	return os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func applyDockerConfigPlatformAdjustments(cfg map[string]any) {
	// Docker Desktop's "desktop" helper can be broken in headless/service runs.
	delete(cfg, "credsStore")
	delete(cfg, "credStore")
	// Connector-owned config does not include Docker CLI context metadata.
	// Keeping currentContext here can break endpoint resolution.
	delete(cfg, "currentContext")
}
