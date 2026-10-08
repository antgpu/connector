//go:build !windows

package main

func maybeHandleWindowsTrayMode(opts runtimeOptions) (bool, error) {
	return false, nil
}
