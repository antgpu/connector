//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"reflect"
	"testing"
)

func TestWindowsServiceRestartArgs(t *testing.T) {
	opts := runtimeOptions{
		ServiceName: "svc-a",
		StateDir:    `C:\ProgramData\Antgpu\Custom`,
		Token:       "enroll-token",
	}
	got := windowsServiceRestartArgs(opts)
	want := []string{"--service-run", "--service-name", "svc-a", "--state-dir", `C:\ProgramData\Antgpu\Custom`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windowsServiceRestartArgs=%v want %v", got, want)
	}
}

func TestWindowsServiceCommandLineRoundTrip(t *testing.T) {
	want := []string{`C:\Program Files\Antgpu\connect.exe`, "--service-run", "--service-name", "svc & value", "--state-dir", `C:\ProgramData\custom state`}
	got, err := windows.DecomposeCommandLine(windowsServiceBinaryPath(want[0], want[1:]))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if serviceExecutablePath(windowsServiceBinaryPath(want[0], want[1:])) != want[0] {
		t.Fatal("lost executable path")
	}
	if !windowsServiceUsesState(want, want[len(want)-1]) || windowsServiceUsesState(want, `C:\other`) {
		t.Fatal("service state mismatch not detected")
	}
}
