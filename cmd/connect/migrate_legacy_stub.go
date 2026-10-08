//go:build !linux

package main

import (
	"context"
	"os"

	"github.com/antgpu/connector/internal/updater"
)

func completeLegacyMigration(context.Context, *updater.Manager) error { return nil }

func installedBinaryPath() (string, error) { return os.Executable() }
