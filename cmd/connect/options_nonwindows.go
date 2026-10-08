//go:build !windows

package main

func makeRestartBinary(opts runtimeOptions, requestShutdown func()) func(string) error {
	return func(binaryPath string) error {
		return spawnReplacementBinary(binaryPath)
	}
}
