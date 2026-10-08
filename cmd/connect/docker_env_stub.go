//go:build !windows

package main

func prepareDockerPlatformEnv() error { return nil }

func applyDockerConfigPlatformAdjustments(cfg map[string]any) {
}
