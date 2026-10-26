//go:build windows

package hw

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/client"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var windowsStorageCache dockerStorageCache
var errRegistryDockerDataMissing = errors.New("Docker data disk was not found in the WSL registry")

type dockerWSLRegistration struct{ name, base, diskFile string }

func windowsDockerStorageUsage(daemonHost, daemonID, kernel string) (dockerStorageSample, error) {
	if !strings.Contains(strings.ToLower(kernel), "microsoft") {
		return dockerStorageSample{}, fmt.Errorf("Docker Desktop storage discovery requires a WSL 2 engine")
	}
	key := daemonHost + "|" + daemonID
	return windowsStorageCache.get(key, time.Now().UTC(), func() (dockerStorageSample, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		path, source, err := discoverWindowsDockerDataDisk()
		if err != nil {
			return dockerStorageSample{}, err
		}
		name, err := windows.UTF16PtrFromString(filepath.Dir(path))
		if err != nil {
			return dockerStorageSample{}, fmt.Errorf("Docker backing volume path is invalid")
		}
		var available, total, free uint64
		if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
			return dockerStorageSample{}, fmt.Errorf("Docker backing volume capacity is unavailable")
		}
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithHost(daemonHost), client.WithAPIVersionNegotiation())
		if err != nil {
			return dockerStorageSample{}, fmt.Errorf("Docker storage probe client is unavailable")
		}
		defer cli.Close()
		info, err := cli.Info(ctx)
		if err != nil || info.ID != daemonID {
			return dockerStorageSample{}, fmt.Errorf("Docker engine changed during storage measurement")
		}
		guest := probeStorageDiagnosticGuest(ctx, cli, true)
		guestTotal, totalOK := guest["total_bytes"].(uint64)
		guestFree, freeOK := guest["available_bytes"].(uint64)
		if guest["error"] != nil || guest["cleanup_error"] != nil || !totalOK || !freeOK {
			return dockerStorageSample{}, fmt.Errorf("Docker filesystem capacity is unavailable")
		}
		sample, err := boundedDockerStorage(total, min(available, free), guestTotal, guestFree)
		if err != nil {
			return dockerStorageSample{}, err
		}
		sample.Source, sample.ObservedAt = source, time.Now().UTC()
		return sample, nil
	})
}

// Docker Desktop may run under a different user from the LocalSystem connector.
// Read loaded users' WSL registrations without loading hives or starting WSL.
func readDockerWSLRegistrations() ([]dockerWSLRegistration, error) {
	users, err := registry.OpenKey(registry.USERS, "", registry.READ)
	if err != nil {
		return nil, fmt.Errorf("WSL user registry is unavailable")
	}
	defer users.Close()
	return dockerWSLRegistrationsFrom(users)
}

func dockerWSLRegistrationsFrom(users registry.Key) ([]dockerWSLRegistration, error) {
	sids, err := users.ReadSubKeyNames(-1)
	if err != nil || len(sids) > 128 {
		return nil, fmt.Errorf("WSL user registry could not be enumerated")
	}
	sort.Strings(sids)
	registrations := []dockerWSLRegistration{}
	for _, sid := range sids {
		if !strings.HasPrefix(sid, "S-1-") || strings.HasSuffix(sid, "_Classes") {
			continue
		}
		wsl, err := registry.OpenKey(users, sid+`\Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.READ)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("WSL registration is not readable")
		}
		ids, readErr := wsl.ReadSubKeyNames(-1)
		if readErr != nil || len(ids) > 64 {
			wsl.Close()
			return nil, fmt.Errorf("WSL registrations could not be enumerated")
		}
		for _, id := range ids {
			entry, err := registry.OpenKey(wsl, id, registry.QUERY_VALUE)
			if err != nil {
				wsl.Close()
				return nil, fmt.Errorf("WSL registration is not readable")
			}
			name, _, _ := entry.GetStringValue("DistributionName")
			base, _, _ := entry.GetStringValue("BasePath")
			file, _, _ := entry.GetStringValue("VhdFileName")
			entry.Close()
			if strings.EqualFold(name, "docker-desktop") || strings.EqualFold(name, "docker-desktop-data") {
				registrations = append(registrations, dockerWSLRegistration{name, base, file})
			}
		}
		wsl.Close()
	}
	return registrations, nil
}

func dockerWSLDataCandidates(registrations []dockerWSLRegistration) []string {
	out := []string{}
	for _, r := range registrations {
		base := strings.TrimPrefix(r.base, `\\?\`)
		if !diagnosticLocalPath(base) {
			continue
		}
		base = filepath.Clean(base)
		if strings.EqualFold(r.name, "docker-desktop-data") {
			file := r.diskFile
			if file == "" {
				file = "ext4.vhdx"
			}
			if filepath.Base(file) == file && strings.EqualFold(filepath.Ext(file), ".vhdx") {
				out = append(out, filepath.Join(base, file))
			}
		} else if strings.EqualFold(r.name, "docker-desktop") {
			// The registered main/ext4.vhdx is the boot disk, not workload
			// storage. Newer Desktop versions use a sibling data disk.
			parent := filepath.Dir(base)
			for _, path := range []string{`disk\docker_data.vhdx`, `data\docker_data.vhdx`, `data\ext4.vhdx`} {
				out = append(out, filepath.Join(parent, path))
			}
		}
	}
	return out
}

func selectDockerDataDisk(candidates []string) (string, error) {
	paths := map[string]string{}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("Docker data disk path is not readable")
		}
		if !diagnosticLocalPath(resolved) {
			return "", fmt.Errorf("Docker data disk is not on a local volume")
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("Docker data disk is not a regular readable file")
		}
		paths[strings.ToLower(resolved)] = resolved
	}
	if len(paths) == 0 {
		return "", errRegistryDockerDataMissing
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("multiple Docker data disks were found; the active backing volume is ambiguous")
	}
	for _, path := range paths {
		return path, nil
	}
	panic("unreachable")
}

func discoverWindowsDockerDataDisk() (string, string, error) {
	entries, err := readDockerWSLRegistrations()
	if err != nil {
		return "", "", err
	}
	path, err := selectDockerDataDisk(dockerWSLDataCandidates(entries))
	if err == nil {
		return path, "docker_desktop_wsl_registry_bounded", nil
	}
	if !errors.Is(err, errRegistryDockerDataMissing) {
		return "", "", err
	}
	// Preserve the established default layouts when no relocated data disk is
	// registered. An unreadable or ambiguous registry result never falls back.
	if path := dockerDesktopWSLDataPath(); path != "" {
		resolved, err := selectDockerDataDisk([]string{path})
		return resolved, "docker_desktop_wsl_default_bounded", err
	}
	return "", "", errRegistryDockerDataMissing
}
