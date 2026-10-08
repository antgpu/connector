//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func clearPersistedServiceEnrollmentToken(opts runtimeOptions) error {
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service %q: %w", serviceName, err)
	}
	defer s.Close()

	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("read service %q config: %w", serviceName, err)
	}

	binaryPath := strings.TrimSpace(cfg.BinaryPathName)
	if binaryPath == "" {
		return nil
	}

	exe := serviceExecutablePath(cfg.BinaryPathName)
	if exe == "" {
		return fmt.Errorf("invalid service executable path")
	}
	cfg.BinaryPathName = windowsServiceBinaryPath(exe, windowsServiceRestartArgs(opts))
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("update service %q binary path: %w", serviceName, err)
	}
	return nil
}

func serviceExecutablePath(binaryPath string) string {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return ""
	}

	args, err := windows.DecomposeCommandLine(binaryPath)
	if err != nil || len(args) == 0 {
		return ""
	}
	return args[0]
}
