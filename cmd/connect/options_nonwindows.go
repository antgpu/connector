//go:build !windows

package main

import (
	"os"
	"runtime"
)

func makeRestartBinary(opts runtimeOptions, requestShutdown func()) func(string) error {
	return func(binaryPath string) error {
		if runtime.GOOS == "linux" {
			target := os.Getenv("ANTGPU_CONNECT_INSTALL_PATH")
			if target == "" {
				var err error
				target, err = os.Executable()
				if err != nil {
					return err
				}
			}
			if err := installReplacementBinary(binaryPath, target); err != nil {
				return err
			}
			binaryPath = target
		}
		return spawnReplacementBinary(binaryPath)
	}
}
