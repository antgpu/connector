package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/antgpu/connector/internal/securefs"
)

func prepareStateWorkingDir(opts runtimeOptions) error {
	if isWindowsTrayInvocation(opts) {
		return nil
	}
	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		return nil
	}

	absDir, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state dir %q: %w", stateDir, err)
	}
	if err := securefs.PrivateDir(absDir); err != nil {
		return fmt.Errorf("protect state dir %q: %w", absDir, err)
	}
	if err := os.Chdir(absDir); err != nil {
		return fmt.Errorf("chdir state dir %q: %w", absDir, err)
	}
	for _, path := range []string{MachineAuthPath, TunnelConfigPath, "connect.log"} {
		if err := secureExistingStateFile(path); err != nil {
			return err
		}
	}
	return nil
}

func secureExistingStateFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return securefs.PrivateFile(path)
}
