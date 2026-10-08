//go:build linux

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/antgpu/connector/internal/updater"
	"github.com/godbus/dbus/v5"
)

func completeLegacyMigration(ctx context.Context, manager *updater.Manager) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !isLegacyCachedExecutable(executable, os.TempDir(), AppVersion, runtime.GOARCH) {
		return nil
	}
	target, err := installedBinaryPath()
	if err != nil {
		return err
	}
	if target == executable {
		return fmt.Errorf("cannot discover stable service executable; rerun the state-preserving installer")
	}
	running, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer running.Close()
	if err := persistLegacyCandidate(running, filepath.Join("cache", "migration"), target, func(path string) error {
		return manager.VerifyRunningCandidate(ctx, path)
	}); err != nil {
		return err
	}
	log.Printf("connector migration: authenticated current executable installed at %s; restarting in place", target)
	return spawnReplacementBinary(target)
}

func installedBinaryPath() (string, error) {
	if target := strings.TrimSpace(os.Getenv("ANTGPU_CONNECT_INSTALL_PATH")); target != "" {
		if !filepath.IsAbs(target) || filepath.Clean(target) != target {
			return "", fmt.Errorf("installed connector path must be absolute and clean")
		}
		return target, nil
	}
	if strings.TrimSpace(os.Getenv("INVOCATION_ID")) == "" {
		return os.Executable()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("connect systemd bus: %w", err)
	}
	defer conn.Close()
	var unit dbus.ObjectPath
	if err := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").CallWithContext(ctx,
		"org.freedesktop.systemd1.Manager.GetUnitByPID", 0, uint32(os.Getpid())).Store(&unit); err != nil {
		return "", fmt.Errorf("discover connector service: %w", err)
	}
	object := conn.Object("org.freedesktop.systemd1", unit)
	getAll := func(iface string) (map[string]dbus.Variant, error) {
		var properties map[string]dbus.Variant
		err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, iface).Store(&properties)
		return properties, err
	}
	unitProps, err := getAll("org.freedesktop.systemd1.Unit")
	if err != nil {
		return "", err
	}
	serviceProps, err := getAll("org.freedesktop.systemd1.Service")
	if err != nil {
		return "", err
	}
	invocation, _ := unitProps["InvocationID"].Value().([]byte)
	mainPID, _ := serviceProps["MainPID"].Value().(uint32)
	if mainPID != uint32(os.Getpid()) || len(invocation) != 16 || hex.EncodeToString(invocation) != os.Getenv("INVOCATION_ID") {
		return "", fmt.Errorf("connector is not the main process of this systemd invocation")
	}
	var commands []struct {
		Path                                                       string
		Args                                                       []string
		IgnoreFailure                                              bool
		StartTime, StartTimeMonotonic, ExitTime, ExitTimeMonotonic uint64
		PID                                                        uint32
		Code, Status                                               int32
	}
	if err := dbus.Store([]interface{}{serviceProps["ExecStart"].Value()}, &commands); err != nil || len(commands) != 1 {
		return "", fmt.Errorf("expected one connector service ExecStart command")
	}
	return serviceExecutable(commands[0].Path, commands[0].Args)
}
