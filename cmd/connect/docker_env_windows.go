//go:build windows

package main

func applyDockerConfigPlatformAdjustments(cfg map[string]any) {
	// Docker Desktop's "desktop" helper can be broken in headless/service runs.
	delete(cfg, "credsStore")
	delete(cfg, "credStore")
	// Connector-owned config does not include Docker CLI context metadata.
	// Keeping currentContext here can break endpoint resolution.
	delete(cfg, "currentContext")
}
