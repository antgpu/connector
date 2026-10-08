//go:build windows

package securefs

import "os"

// Windows ACL hardening is a separate, deferred release requirement.
func checkOwner(os.FileInfo, bool) error { return nil }
