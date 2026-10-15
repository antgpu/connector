package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallNoPopup(t *testing.T) {
	for _, args := range [][]string{
		{"--service-install", "--no-popup", "enrollment-fixture"},
		{"--service-install", "--no-popup"},
	} {
		opts, err := parseRuntimeOptions(args)
		if err != nil || !opts.ServiceInstall || !opts.NoPopup {
			t.Fatalf("parse install options: %+v, %v", opts, err)
		}
		if len(args) == 3 && opts.Token != "enrollment-fixture" {
			t.Fatal("popup flag consumed the enrollment token")
		}
	}
	opts, err := parseRuntimeOptions([]string{"--service-install"})
	if err != nil || opts.NoPopup {
		t.Fatalf("default installation must retain its log window: %+v, %v", opts, err)
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, showLogsOnceMarker)
	logPath := filepath.Join(dir, "connect.log")
	if err := os.WriteFile(logPath, []byte("existing logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A quiet fresh installation must not leave a deferred popup request.
	if err := markShowLogsOnce(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("quiet installation left a marker: %v", err)
	}
	// Default installation still requests the viewer; a later quiet repair
	// must remove that unconsumed request before starting the service.
	if err := markShowLogsOnce(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	if err := markShowLogsOnce(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("quiet repair left a stale popup request: %v", err)
	}
	if data, err := os.ReadFile(logPath); err != nil || string(data) != "existing logs" {
		t.Fatalf("quiet install changed existing logs: %q, %v", data, err)
	}
}

func TestInstallNoPopupPropagatesMarkerRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, showLogsOnceMarker)
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "unexpected"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markShowLogsOnce(dir, true); err == nil {
		t.Fatal("installer must fail if it cannot clear a stale popup request")
	}
}
