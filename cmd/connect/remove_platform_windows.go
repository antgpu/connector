//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func cleanupConnectorPlatformHooks(opts runtimeOptions) error {
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}

	var errs []string
	if err := removeTrayAutoRun(serviceName); err != nil {
		errs = append(errs, fmt.Sprintf("remove tray autorun: %v", err))
	}
	if err := deleteWindowsServiceRegistration(serviceName); err != nil {
		errs = append(errs, fmt.Sprintf("delete service registration: %v", err))
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func deleteWindowsServiceRegistration(serviceName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return fmt.Errorf("open service %q: %w", serviceName, err)
	}
	defer s.Close()

	if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete service %q: %w", serviceName, err)
	}
	return nil
}
