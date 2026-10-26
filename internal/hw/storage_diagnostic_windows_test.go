//go:build windows

package hw

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDiagnosticWindowsPathSafetyAndRedaction(t *testing.T) {
	profiles := []diagnosticProfile{{alias: "profile_A", path: `D:\Profiles\PrivateName`}}
	for _, path := range []string{`\\server\share\docker.vhdx`, `\\?\C:\docker.vhdx`, `relative\docker.vhdx`, `C:relative`, "C:\\bad\nname"} {
		if diagnosticLocalPath(path) {
			t.Errorf("accepted nonlocal/ambiguous path %q", path)
		}
	}
	if !diagnosticLocalPath(`D:\Docker\disk\docker_data.vhdx`) {
		t.Fatal("rejected relocated disk")
	}
	if got := diagnosticRedactPath(`d:\profiles\privatename\AppData\Local\Docker`, profiles); got != `%profile_A%\AppData\Local\Docker` {
		t.Fatalf("profile identity leaked: %q", got)
	}
	if got := diagnosticRedactPath(`D:\Profiles\PrivateNameExtra\Docker`, profiles); got != `D:\Profiles\PrivateNameExtra\Docker` {
		t.Fatal("redacted a different path prefix")
	}
}

func TestDiagnosticDistinguishesMissingAndDenied(t *testing.T) {
	for err, want := range map[error]string{os.ErrNotExist: "missing", windows.ERROR_PATH_NOT_FOUND: "missing", windows.ERROR_ACCESS_DENIED: "access_denied", os.ErrPermission: "access_denied"} {
		if got := diagnosticErrorClass(err); got != want {
			t.Errorf("%v: got %q want %q", err, got, want)
		}
	}
}
