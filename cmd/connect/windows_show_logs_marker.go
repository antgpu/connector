package main

import (
	"os"
	"path/filepath"
	"time"
)

const showLogsOnceMarker = ".show-logs-once"

func markShowLogsOnce(stateDir string, noPopup bool) error {
	marker := filepath.Join(stateDir, showLogsOnceMarker)
	if noPopup {
		// Also clear an unconsumed request from an earlier installation before
		// SCM starts the service. Removing it after startup would race the tray.
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644)
}
