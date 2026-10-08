//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func cleanupConnectorPlatformHooks(opts runtimeOptions) error {
	if strings.TrimSpace(os.Getenv("INVOCATION_ID")) == "" {
		return nil
	}
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}
	dropInDir := filepath.Join("/etc/systemd/system", serviceName+".service.d")
	if err := os.MkdirAll(dropInDir, 0o755); err != nil {
		return fmt.Errorf("create systemd drop-in dir: %w", err)
	}
	dropInPath := filepath.Join(dropInDir, "99-antgpu-removed.conf")
	content := []byte("[Service]\nRestart=no\n")
	if err := os.WriteFile(dropInPath, content, 0o644); err != nil {
		return fmt.Errorf("write systemd drop-in: %w", err)
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl("disable", serviceName+".service"); err != nil {
		return err
	}
	return nil
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
