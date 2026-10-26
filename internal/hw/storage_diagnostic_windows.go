//go:build windows

package hw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	psDisk "github.com/shirou/gopsutil/v4/disk"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type diagnosticProfile struct{ alias, sid, path string }
type diagnosticPath struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Total  uint64 `json:"total_bytes,omitempty"`
	Free   uint64 `json:"free_bytes,omitempty"`
}
type diagnosticSettings struct {
	Profile string         `json:"profile"`
	File    string         `json:"file"`
	Status  string         `json:"status"`
	Values  map[string]any `json:"values,omitempty"`
}
type diagnosticWindowsHost struct {
	Identity      string               `json:"identity_category"`
	ProfileCount  int                  `json:"profile_count"`
	ProfileStatus string               `json:"profile_status"`
	Settings      []diagnosticSettings `json:"settings"`
	Paths         []diagnosticPath     `json:"paths"`
	WSL           []map[string]string  `json:"wsl_registry"`
	Truncated     bool                 `json:"truncated"`
}

func diagnosticErrorClass(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return "missing"
	case errors.Is(err, os.ErrPermission), errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "access_denied"
	default:
		return "unavailable"
	}
}

func diagnosticWindowsProfiles() ([]diagnosticProfile, string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`, registry.READ)
	if err != nil {
		return nil, diagnosticErrorClass(err)
	}
	defer key.Close()
	sids, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, diagnosticErrorClass(err)
	}
	sort.Strings(sids)
	profiles := []diagnosticProfile{}
	for _, sid := range sids {
		if len(profiles) >= 24 {
			break
		}
		child, err := registry.OpenKey(key, sid, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		path, _, err := child.GetStringValue("ProfileImagePath")
		child.Close()
		if err != nil {
			continue
		}
		path, err = registry.ExpandString(path)
		if err != nil || !diagnosticLocalPath(path) {
			continue
		}
		profiles = append(profiles, diagnosticProfile{alias: "profile_" + string(rune('A'+len(profiles))), sid: sid, path: filepath.Clean(path)})
	}
	return profiles, "ok"
}

// Never query a UNC share or enumerate whole drives. Only local paths read from
// the profile registry, Docker's allowlisted settings, and known disk layouts.
func diagnosticLocalPath(path string) bool {
	volume := filepath.VolumeName(path)
	return len(volume) == 2 && volume[1] == ':' && filepath.IsAbs(path) && !strings.ContainsAny(path, "\x00\r\n")
}

func diagnosticRedactPath(path string, profiles []diagnosticProfile) string {
	best := -1
	for i, p := range profiles {
		if strings.EqualFold(path, p.path) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(p.path)+`\`) {
			if best < 0 || len(p.path) > len(profiles[best].path) {
				best = i
			}
		}
	}
	if best >= 0 {
		p := profiles[best]
		return "%" + p.alias + "%" + path[len(p.path):]
	}
	return path
}

func collectStorageDiagnosticHost(ctx context.Context) any {
	profiles, status := diagnosticWindowsProfiles()
	out := diagnosticWindowsHost{Identity: "other", ProfileCount: len(profiles), ProfileStatus: status}
	if user, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		switch user.User.Sid.String() {
		case "S-1-5-18":
			out.Identity = "local_system"
		case "S-1-5-19":
			out.Identity = "local_service"
		case "S-1-5-20":
			out.Identity = "network_service"
		}
	}
	seen := map[string]bool{}
	probe := func(kind, path string) {
		if len(out.Paths) >= 128 || ctx.Err() != nil {
			out.Truncated = true
			return
		}
		path = filepath.Clean(path)
		if !diagnosticLocalPath(path) || seen[strings.ToLower(path)] {
			return
		}
		seen[strings.ToLower(path)] = true
		result := diagnosticPath{Kind: kind, Path: diagnosticRedactPath(path, profiles)}
		info, err := os.Stat(path)
		result.Status = diagnosticErrorClass(err)
		if err == nil {
			dir := path
			if !info.IsDir() {
				dir = filepath.Dir(path)
			}
			usage, err := psDisk.Usage(dir)
			result.Status = "found_capacity_" + diagnosticErrorClass(err)
			if err == nil {
				result.Total, result.Free = usage.Total, usage.Free
			}
		}
		out.Paths = append(out.Paths, result)
	}
	probeDiskLayouts := func(kind, base string) {
		if !diagnosticLocalPath(base) {
			return
		}
		probe(kind+"_configured_path", base)
		if strings.EqualFold(filepath.Ext(base), ".vhdx") {
			return
		}
		for _, relative := range []string{`docker_data.vhdx`, `ext4.vhdx`, `disk\docker_data.vhdx`, `data\ext4.vhdx`, `main\ext4.vhdx`, `DockerDesktop.vhdx`} {
			probe(kind+"_vhdx", filepath.Join(base, relative))
		}
	}
	for _, profile := range profiles {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		for _, filename := range []string{"settings-store.json", "settings.json"} {
			path := filepath.Join(profile.path, "AppData", "Roaming", "Docker", filename)
			entry := diagnosticSettings{Profile: profile.alias, File: filename}
			file, err := os.Open(path)
			entry.Status = diagnosticErrorClass(err)
			if err == nil {
				var values map[string]json.RawMessage
				err = json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&values)
				file.Close()
				if err != nil {
					entry.Status = "invalid_or_oversized_json"
				} else {
					entry.Values = map[string]any{}
					for key, raw := range values {
						switch strings.ToLower(key) {
						case "datafolder", "diskimagelocation":
							var path string
							if json.Unmarshal(raw, &path) == nil && len(path) < 1024 {
								if diagnosticLocalPath(path) {
									entry.Values[key] = diagnosticRedactPath(path, profiles)
									probeDiskLayouts("desktop_settings", path)
								} else {
									entry.Values[key] = "non_local_or_unexpanded_path"
								}
							}
						case "wslengineenabled", "usewindowscontainers", "enhancedcontainerisolation", "usecontainerdsnapshotter":
							var enabled bool
							if json.Unmarshal(raw, &enabled) == nil {
								entry.Values[key] = enabled
							}
						}
					}
				}
			}
			out.Settings = append(out.Settings, entry)
		}
		probeDiskLayouts("default_wsl", filepath.Join(profile.path, "AppData", "Local", "Docker", "wsl"))
		probe("default_vmm_vhdx", filepath.Join(profile.path, "AppData", "Local", "Docker", "vm-data", "DockerDesktop.vhdx"))
		wsl, err := registry.OpenKey(registry.USERS, profile.sid+`\Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.READ)
		if err != nil {
			out.WSL = append(out.WSL, map[string]string{"profile": profile.alias, "status": diagnosticErrorClass(err)})
			continue
		}
		ids, _ := wsl.ReadSubKeyNames(32)
		for _, id := range ids {
			key, err := registry.OpenKey(wsl, id, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			name, _, _ := key.GetStringValue("DistributionName")
			base, _, _ := key.GetStringValue("BasePath")
			key.Close()
			if name != "docker-desktop" && name != "docker-desktop-data" {
				continue
			}
			base = strings.TrimPrefix(base, `\\?\`)
			out.WSL = append(out.WSL, map[string]string{"profile": profile.alias, "distribution": name, "base_path": diagnosticRedactPath(base, profiles), "status": "registered"})
			probeDiskLayouts("wsl_registry", base)
		}
		wsl.Close()
	}
	if base := os.Getenv("ProgramData"); diagnosticLocalPath(base) {
		probe("default_hyperv_vhdx", filepath.Join(base, "DockerDesktop", "vm-data", "DockerDesktop.vhdx"))
	}
	return out
}
