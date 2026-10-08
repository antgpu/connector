//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
)

func clearPersistedServiceEnrollmentToken(opts runtimeOptions) error {
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}
	envPath := "/etc/default/" + serviceName
	data, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", envPath, err)
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ANTGPU_CONNECT_TOKEN=") || strings.HasPrefix(trimmed, "export ANTGPU_CONNECT_TOKEN=") {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return nil
	}
	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	_ = os.Chmod(envPath, 0o600)
	return nil
}
