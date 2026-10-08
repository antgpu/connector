//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/antgpu/connector/internal/securefs"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
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
	var err error
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		return err
	}
	if err := securefs.ServiceExecutable(binaryPath); err != nil {
		return fmt.Errorf("unsafe replacement executable: %w", err)
	}
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

	previous := cfg
	cfg.BinaryPathName = windowsServiceBinaryPath(binaryPath, windowsServiceRestartArgs(opts))
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("update service %q binary path: %w", serviceName, err)
	}

	// A native helper waits for SCM's Stopped state. There is no command shell,
	// fixed timing assumption, or enrollment token in the restart arguments.
	restartCmd := exec.Command(binaryPath, "--service-restart", "--service-name", serviceName, "--state-dir", opts.StateDir)
	restartCmd.Stdout = nil
	restartCmd.Stderr = nil
	restartCmd.Stdin = nil
	if err := restartCmd.Start(); err != nil {
		return errors.Join(fmt.Errorf("schedule service %q restart: %w", serviceName, err), s.UpdateConfig(previous))
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

func waitAndStartWindowsService(serviceName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
				return err
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for service %q to stop before update restart", serviceName)
}
