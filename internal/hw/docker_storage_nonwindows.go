//go:build !windows

package hw

import "fmt"

func windowsDockerStorageUsage(string, string, string) (dockerStorageSample, error) {
	return dockerStorageSample{}, fmt.Errorf("Windows storage discovery is unavailable on this platform")
}
