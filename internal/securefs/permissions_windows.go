//go:build windows

package securefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

func currentSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

func trustedSID(sid, current string) bool {
	return sid == current || sid == "S-1-5-18" || sid == "S-1-5-32-544" || sid == trustedInstallerSID
}

// Open the object itself, never a junction/symlink target. Keeping this handle
// open without FILE_SHARE_DELETE also prevents renaming it during inspection.
func checkedHandle(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, access|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open protected path", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err == nil && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		err = fmt.Errorf("reparse point refused: %s", path)
	}
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func checkACL(h windows.Handle, path string, private, ancestor bool) error {
	return checkACLForUser(h, path, private, ancestor, true)
}

func checkACLForUser(h windows.Handle, path string, private, ancestor, allowCurrent bool) error {
	current, err := currentSID()
	if err != nil {
		return err
	}
	if !allowCurrent {
		current = ""
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return fmt.Errorf("security descriptor unavailable for %s: %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trustedSID(owner.String(), current) {
		return fmt.Errorf("untrusted owner of %s", path)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("missing DACL on %s", path)
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsupported DACL entry on %s", path)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if trustedSID(sid, current) {
			continue
		}
		unsafeMask := uint32(windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | 0x40 /* FILE_DELETE_CHILD */)
		if !ancestor {
			unsafeMask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES
		}
		if private || uint32(ace.Mask)&unsafeMask != 0 {
			return fmt.Errorf("untrusted access granted on %s to %s", path, sid)
		}
	}
	runtime.KeepAlive(sd)
	return nil
}

func trustedAncestors(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		h, err := checkedHandle(p, 0)
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		if err == nil {
			err = checkACL(h, p, false, true)
			windows.CloseHandle(h)
			if err != nil {
				return err
			}
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func privateDescriptor(directory, executable bool) (*windows.SECURITY_DESCRIPTOR, error) {
	current, err := currentSID()
	if err != nil {
		return nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	sddl := "D:P(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;BA)"
	if !windows.GetCurrentProcessToken().IsElevated() && current != "S-1-5-18" {
		sddl += "(A;" + inherit + ";FA;;;" + current + ")"
	}
	if executable {
		// The desktop tray runs with the interactive user's token. It can read
		// the executable but never credentials or modify service code.
		sddl += "(A;;FRFX;;;BU)"
	}
	return windows.SecurityDescriptorFromString(sddl)
}

func setPrivateACL(h windows.Handle, directory, executable bool) error {
	sd, err := privateDescriptor(directory, executable)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	runtime.KeepAlive(sd)
	return err
}

// PrivateDir creates each component with a private DACL from the outset.
// Existing writable/untrusted paths are rejected rather than taken over.
func PrivateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if strings.HasPrefix(abs, `\\`) {
		return fmt.Errorf("network state/cache paths are unsupported")
	}
	if err := trustedAncestors(abs); err != nil {
		return err
	}
	if _, err := os.Lstat(abs); os.IsNotExist(err) {
		if err := PrivateDirParent(filepath.Dir(abs)); err != nil {
			return err
		}
		sd, err := privateDescriptor(true, false)
		if err != nil {
			return err
		}
		p, err := windows.UTF16PtrFromString(abs)
		if err != nil {
			return err
		}
		sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		err = windows.CreateDirectory(p, &sa)
		runtime.KeepAlive(sd)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("invalid private directory %s: %v", abs, err)
	}
	h, err := checkedHandle(abs, windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := checkACL(h, abs, false, false); err != nil {
		return err
	}
	return setPrivateACL(h, true, false)
}

func PrivateDirParent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return trustedAncestors(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return PrivateDir(path)
}

func RegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe file %s", path)
	}
	if err := trustedAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	h, err := checkedHandle(path, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return checkACL(h, path, false, false)
}

// ServiceExecutable must stay safe when run as LocalSystem, including against
// the installing administrator's unelevated desktop token. A user-writable
// download/profile directory is never a service installation destination.
func ServiceExecutable(path string) error {
	if err := RegularFile(path); err != nil {
		return err
	}
	for p := path; ; p = filepath.Dir(p) {
		h, err := checkedHandle(p, 0)
		if err != nil {
			return err
		}
		err = checkACLForUser(h, p, false, p != path, false)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func PrivateFile(path string) error {
	if err := RegularFile(path); err != nil {
		return err
	}
	h, err := checkedHandle(path, windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return setPrivateACL(h, false, false)
}

func ExecutableFile(path string) error {
	if err := RegularFile(path); err != nil {
		return err
	}
	h, err := checkedHandle(path, windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return setPrivateACL(h, false, true)
}
