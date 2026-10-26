//go:build windows

package hw

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestDockerWSLCandidatesExcludeSystemDiskAndUnsafePaths(t *testing.T) {
	entries := []dockerWSLRegistration{
		{"docker-desktop", `\\?\A:\Docker\main`, ""},
		{"docker-desktop-data", `D:\Docker\data`, ""},
		{"docker-desktop-data", `C:\Docker\data`, `..\outside.vhdx`},
		{"docker-desktop-data", `\\server\share`, ""},
		{"Ubuntu", `C:\Ubuntu`, ""},
	}
	got := dockerWSLDataCandidates(entries)
	joined := strings.Join(got, "\n")
	if len(got) != 4 || !strings.Contains(joined, `A:\Docker\disk\docker_data.vhdx`) || !strings.Contains(joined, `D:\Docker\data\ext4.vhdx`) {
		t.Fatalf("relocated/legacy data disks missing: %v", got)
	}
	if strings.Contains(joined, `main\ext4.vhdx`) || strings.Contains(joined, "outside") || strings.Contains(joined, "server") || strings.Contains(joined, "Ubuntu") {
		t.Fatalf("selected a system, unrelated, or unsafe disk: %v", got)
	}
}

func TestSelectDockerDataDiskRejectsAmbiguity(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first.vhdx"), filepath.Join(root, "second.vhdx")
	if err := os.WriteFile(first, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := selectDockerDataDisk([]string{filepath.Join(root, "missing.vhdx"), first, strings.ToUpper(first)})
	if err != nil || !strings.EqualFold(got, first) {
		t.Fatalf("could not deduplicate existing data disk: %q %v", got, err)
	}
	if _, err := selectDockerDataDisk([]string{first, second}); err == nil {
		t.Fatal("selected an ambiguous data disk")
	}
	if _, err := selectDockerDataDisk([]string{filepath.Join(root, "missing.vhdx")}); !errors.Is(err, errRegistryDockerDataMissing) {
		t.Fatalf("unexpected missing result: %v", err)
	}
}

func TestReadDockerRegistrationsFromLoadedUserHive(t *testing.T) {
	path := `Software\AntGPU-Storage-Test-` + strings.ReplaceAll(t.Name(), "/", "-")
	root, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	sid := "S-1-5-21-fixture"
	base := sid + `\Software\Microsoft\Windows\CurrentVersion\Lxss`
	for name, distribution := range map[string]string{"docker": "docker-desktop", "ubuntu": "Ubuntu"} {
		key, _, err := registry.CreateKey(root, base+`\`+name, registry.ALL_ACCESS)
		if err != nil {
			t.Fatal(err)
		}
		if err := key.SetStringValue("DistributionName", distribution); err != nil {
			t.Fatal(err)
		}
		if err := key.SetStringValue("BasePath", `A:\Docker\main`); err != nil {
			t.Fatal(err)
		}
		key.Close()
	}
	defer func() {
		for _, suffix := range []string{base + `\docker`, base + `\ubuntu`, base, sid + `\Software\Microsoft\Windows\CurrentVersion`, sid + `\Software\Microsoft\Windows`, sid + `\Software\Microsoft`, sid + `\Software`, sid} {
			_ = registry.DeleteKey(root, suffix)
		}
		root.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
	}()
	got, err := dockerWSLRegistrationsFrom(root)
	if err != nil || len(got) != 1 || got[0].name != "docker-desktop" || got[0].base != `A:\Docker\main` {
		t.Fatalf("incorrect registry registrations: %v %v", got, err)
	}
}
