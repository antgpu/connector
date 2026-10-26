package main

import "testing"

func TestStorageDiagnosticOnlyRunsOnWindowsCanaries(t *testing.T) {
	for _, c := range []struct {
		version, goos string
		want          bool
	}{
		{"v0.1.2-rc.1", "windows", false},
		{"v0.1.2-storage-diag.1", "windows", true},
		{"v0.1.2-storage-diag.1", "linux", false},
		{"v0.1.2-storage-diag.1", "darwin", false},
	} {
		if got := storageDiagnosticEnabled(c.version, c.goos); got != c.want {
			t.Errorf("%s %s: got %t want %t", c.version, c.goos, got, c.want)
		}
	}
}
