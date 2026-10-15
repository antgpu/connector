//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsTrayFixture(t *testing.T) {
	if os.Getenv("ANTGPU_TEST_TRAY_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			opts, err := parseRuntimeOptions(os.Args[i+1:])
			if err != nil {
				t.Fatal(err)
			}
			if handled, err := maybeHandleWindowsTrayMode(opts); !handled || err != nil {
				t.Fatalf("native tray: handled=%t, err=%v", handled, err)
			}
			return
		}
	}
	t.Fatal("missing tray arguments")
}

// This opt-in integration test must run in an interactive Windows desktop.
// It opens real native windows and uses a unique, nonexistent service name,
// without changing an installed service or enrolling a machine.
func TestWindowsTrayNoPopupVisibility(t *testing.T) {
	if os.Getenv("ANTGPU_TEST_INTERACTIVE_TRAY") != "1" {
		t.Skip("requires an interactive Windows desktop")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getClassName := user32.NewProc("GetClassNameW")
	getWindowPID := user32.NewProc("GetWindowThreadProcessId")
	isVisible := user32.NewProc("IsWindowVisible")
	for _, tc := range []struct {
		name     string
		quiet    bool
		stale    bool
		manual   bool
		wantLogs bool
	}{
		{name: "default-install", wantLogs: true},
		{name: "quiet-fresh-install", quiet: true},
		{name: "quiet-repair-clears-stale-request", quiet: true, stale: true},
		{name: "manual-logs-after-quiet-install", quiet: true, manual: true, wantLogs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.stale {
				if err := markShowLogsOnce(dir, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := markShowLogsOnce(dir, tc.quiet); err != nil {
				t.Fatal(err)
			}
			showLogs := hasShowLogsOnce(dir)
			if tc.manual {
				showLogs = true
			}
			serviceName := "antgpu-popup-fixture-" + filepath.Base(dir)
			args := append([]string{"-test.run=^TestWindowsTrayFixture$", "--"}, trayLaunchArgs(serviceName, dir, showLogs)...)
			child := exec.Command(executable, args...)
			child.Env = append(os.Environ(), "ANTGPU_TEST_TRAY_CHILD=1")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			var traySeen, logsSeen bool
			deadline := time.Now().Add(5 * time.Second)
			callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
				var pid uint32
				getWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
				if pid != uint32(child.Process.Pid) {
					return 1
				}
				var class [256]uint16
				getClassName.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
				switch windows.UTF16ToString(class[:]) {
				case "AntgpuConnectTrayWindow":
					traySeen = true
				case "AntgpuConnectLogsWindow":
					if visible, _, _ := isVisible.Call(hwnd); visible != 0 {
						logsSeen = true
					}
				}
				return 1
			})
			for time.Now().Before(deadline) {
				enumWindows.Call(callback, 0)
				if tc.wantLogs && logsSeen {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !traySeen || logsSeen != tc.wantLogs {
				t.Fatalf("native UI: tray=%t, visible log window=%t, want logs=%t", traySeen, logsSeen, tc.wantLogs)
			}
		})
	}
}
