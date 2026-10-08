//go:build !windows

package main

import (
	"runtime"
)

func makeRestartBinary(opts runtimeOptions, requestShutdown func()) func(string) error {
	return func(binaryPath string) error {
		if runtime.GOOS == "linux" {
			target, err := installedBinaryPath()
			if err != nil {
				return err
			}
			if err := installReplacementBinary(binaryPath, target); err != nil {
				return err
			}
			binaryPath = target
		}
		return spawnReplacementBinary(binaryPath)
	}
}
