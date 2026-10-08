//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

func makeRestartBinary(opts runtimeOptions, requestShutdown func()) func(string) error {
	return func(binaryPath string) error {
		if opts.RunningAsService {
			return restartWindowsServiceBinary(binaryPath, opts, requestShutdown)
		}
		return spawnReplacementBinary(binaryPath)
	}
}

func windowsServiceRestartArgs(opts runtimeOptions) []string {
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = defaultWindowsStateDir()
	}

	return []string{"--service-run", "--service-name", serviceName, "--state-dir", stateDir}
}

func windowsServiceBinaryPath(binaryPath string, args []string) string {
	parts := []string{syscall.EscapeArg(binaryPath)}
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func restartWindowsServiceBinary(binaryPath string, opts runtimeOptions, requestShutdown func()) error {
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

	cfg.BinaryPathName = windowsServiceBinaryPath(binaryPath, windowsServiceRestartArgs(opts))
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("update service %q binary path: %w", serviceName, err)
	}

	restartCmd := exec.Command("cmd", "/C", fmt.Sprintf("ping 127.0.0.1 -n 3 >NUL && sc start %s >NUL", syscall.EscapeArg(serviceName)))
	restartCmd.Stdout = nil
	restartCmd.Stderr = nil
	restartCmd.Stdin = nil
	if err := restartCmd.Start(); err != nil {
		return fmt.Errorf("schedule service %q restart: %w", serviceName, err)
	}
	_ = restartCmd.Process.Release()

	go func() {
		time.Sleep(200 * time.Millisecond)
		if requestShutdown != nil {
			requestShutdown()
		}
	}()

	return nil
}
