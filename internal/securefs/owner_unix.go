//go:build !windows

package securefs

import (
	"fmt"
	"os"
	"syscall"
)

func checkOwner(info os.FileInfo, exact bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("file ownership unavailable")
	}
	uid := uint32(os.Geteuid())
	if stat.Uid != uid && (exact || stat.Uid != 0) {
		return fmt.Errorf("unexpected owner %d", stat.Uid)
	}
	return nil
}
