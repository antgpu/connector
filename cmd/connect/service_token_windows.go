//go:build windows

package main

import (
	"fmt"
	"strings"
	"unsafe"

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

	cfg.BinaryPathName = windowsServiceBinaryPath(serviceExecutablePath(cfg.BinaryPathName), windowsServiceRestartArgs(opts))
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

	cmdPtr, err := windows.UTF16PtrFromString(binaryPath)
	if err != nil {
		return binaryPath
	}

	var argc int32
	argv, err := windows.CommandLineToArgv(cmdPtr, &argc)
	if err != nil || argc <= 0 {
		return binaryPath
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(argv)))

	args := (*[8192]*uint16)(unsafe.Pointer(argv))[:argc:argc]
	if len(args) == 0 || args[0] == nil {
		return binaryPath
	}
	return windows.UTF16PtrToString(args[0])
}
