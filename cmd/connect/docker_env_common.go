package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dockerHubIndex = "https://index.docker.io/v1/"
const templateScriptsDirEnv = "CONNECT_TEMPLATE_SCRIPTS_DIR"

func prepareDockerCLIEnv(opts runtimeOptions) error {
	if isWindowsTrayInvocation(opts) || opts.ServiceRestart {
		return nil
	}
	if err := prepareDockerPlatformEnv(); err != nil {
		return err
	}
	if strings.TrimSpace(os.Getenv("DOCKER_CONFIG")) != "" {
		return nil
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = "."
	}
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state dir %q: %w", stateDir, err)
	}
	stateDir = absStateDir

	configDir := filepath.Join(stateDir, "docker-config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create docker config dir %q: %w", configDir, err)
	}
	if err := os.Chmod(configDir, 0o700); err != nil {
		return fmt.Errorf("chmod docker config dir %q: %w", configDir, err)
	}
	configPath := filepath.Join(configDir, "config.json")

	finalCfg := map[string]any{
		"auths": map[string]any{
			dockerHubIndex: map[string]any{},
		},
	}
	applyDockerConfigPlatformAdjustments(finalCfg)

	data, err := json.MarshalIndent(finalCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal docker config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write docker config %q: %w", configPath, err)
	}
	_ = os.Chmod(configPath, 0o600)

	if err := os.Setenv("DOCKER_CONFIG", configDir); err != nil {
		return fmt.Errorf("set DOCKER_CONFIG: %w", err)
	}
	scriptsDir := filepath.Join(stateDir, "template-manager", "scripts")
	if err := os.Setenv(templateScriptsDirEnv, scriptsDir); err != nil {
		return fmt.Errorf("set %s: %w", templateScriptsDirEnv, err)
	}
	return nil
}
