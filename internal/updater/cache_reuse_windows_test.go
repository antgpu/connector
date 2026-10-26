//go:build windows

package updater

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReuseWindowsCachedArtifactWithDeleteSharingDenied(t *testing.T) {
	m, path, item := cachedRollbackFixture(t)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	got, err := m.downloadAndCache(context.Background(), "rollback-test", item)
	if err != nil || got != path {
		t.Fatalf("locked, verified Windows executable could not be reused: %q %v", got, err)
	}
}
