//go:build windows

package securefs

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateACLAndUntrustedWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := PrivateDir(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credentials")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrivateFile(path); err != nil {
		t.Fatal(err)
	}
	h, err := checkedHandle(path, windows.WRITE_DAC)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := checkACL(h, path, true, false); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, _ := sd.DACL()
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := RegularFile(path); err == nil {
		t.Fatal("accepted Everyone-writable executable")
	}
}

func TestWindowsReparseAncestorRejected(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink creation privilege unavailable: %v", err)
	}
	if err := PrivateDir(filepath.Join(link, "cache")); err == nil {
		t.Fatal("followed reparse ancestor")
	}
}

func TestWindowsPrivateCredentialsDeniedToFilteredAdministrator(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an elevated interactive administrator with a linked token")
	}
	adminSID, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	disabled := windows.SIDAndAttributes{Sid: adminSID}
	var linked windows.Token
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	var primary windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY, &primary); err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	ok, _, callErr := create.Call(uintptr(primary), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&linked)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	defer linked.Close()
	root := filepath.Join(t.TempDir(), "private")
	if err := PrivateDir(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credentials")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrivateFile(path); err != nil {
		t.Fatal(err)
	}
	// Check the same object with the filtered administrator's impersonation
	// token through the OS, without creating a local test user.
	var duplicate windows.Token
	if err := windows.DuplicateTokenEx(linked, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &duplicate); err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := sd.ToAbsolute()
	if err != nil {
		t.Fatal(err)
	}
	type genericMapping struct{ GenericRead, GenericWrite, GenericExecute, GenericAll uint32 }
	mapping := genericMapping{GenericRead: windows.FILE_GENERIC_READ, GenericWrite: windows.FILE_GENERIC_WRITE, GenericExecute: windows.FILE_GENERIC_EXECUTE, GenericAll: 0x1f01ff}
	var privileges [1024]byte
	size := uint32(len(privileges))
	var granted, allowed uint32
	proc := windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")
	ok, _, callErr = proc.Call(uintptr(unsafe.Pointer(abs)), uintptr(duplicate), windows.FILE_READ_DATA, uintptr(unsafe.Pointer(&mapping)), uintptr(unsafe.Pointer(&privileges[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&allowed)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	if allowed != 0 {
		t.Fatal("filtered administrator can read machine credentials")
	}
}

func TestWindowsServiceExecutableRejectsDesktopUserWrite(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires elevated administrator")
	}
	root, err := os.MkdirTemp(os.Getenv("ProgramData"), "antgpu-acl-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "service.exe")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ServiceExecutable(path); err != nil {
		t.Fatal(err)
	}
	current, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + current + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, _ := sd.DACL()
	h, err := checkedHandle(path, windows.WRITE_DAC)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := ServiceExecutable(path); err == nil {
		t.Fatal("accepted service code writable by the desktop account")
	}
}
