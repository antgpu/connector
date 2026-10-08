//go:build windows

package main

import (
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
