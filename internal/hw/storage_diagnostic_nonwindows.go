//go:build !windows

package hw

import "context"

func collectStorageDiagnosticHost(context.Context) any {
	return map[string]string{"status": "windows_only"}
}
