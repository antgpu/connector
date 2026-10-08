package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func prepareStateWorkingDir(opts runtimeOptions) error {
	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		return nil
	}

	absDir, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state dir %q: %w", stateDir, err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return fmt.Errorf("create state dir %q: %w", absDir, err)
	}
	if err := os.Chmod(absDir, 0o700); err != nil {
		return fmt.Errorf("chmod state dir %q: %w", absDir, err)
	}
	if err := os.Chdir(absDir); err != nil {
		return fmt.Errorf("chdir state dir %q: %w", absDir, err)
	}
	secureExistingStateFile(MachineAuthPath)
	secureExistingStateFile(TunnelConfigPath)
	secureExistingStateFile("connect.log")
	return nil
}

func secureExistingStateFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0o600)
	}
}
