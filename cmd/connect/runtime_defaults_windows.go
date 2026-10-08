//go:build windows

package main

import "strings"

func applyRuntimeDefaults(opts *runtimeOptions) {
	if opts == nil {
		return
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		opts.StateDir = defaultWindowsStateDir()
	}
}
