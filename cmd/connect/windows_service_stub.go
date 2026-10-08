//go:build !windows

package main

func maybeHandleWindowsServiceMode(opts runtimeOptions) (bool, error) {
	return false, nil
}
