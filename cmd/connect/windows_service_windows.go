//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	internaldocker "github.com/antgpu/connector/internal/docker"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

func maybeHandleWindowsServiceMode(opts runtimeOptions) (bool, error) {
	svcName := strings.TrimSpace(opts.ServiceName)
	if svcName == "" {
		svcName = "antgpu-connect"
	}

	if opts.ServiceInstall {
		return true, installWindowsService(opts, svcName)
	}
	if opts.ServiceUninstall {
		return true, uninstallWindowsService(svcName)
	}
	if opts.ServiceStart {
		return true, startWindowsService(svcName)
	}
	if opts.ServiceStop {
		return true, stopWindowsService(svcName, 20*time.Second)
	}
	if opts.ServiceRun {
		return true, runAsWindowsService(opts, svcName)
	}

	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("detect Windows service context: %w", err)
	}
	if isSvc {
		return true, runAsWindowsService(opts, svcName)
	}

	return false, nil
}

func installWindowsService(opts runtimeOptions, serviceName string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		// Non-fatal; fall back to raw executable path.
		exePath, _ = os.Executable()
	}
	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = defaultWindowsStateDir()
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state dir %q: %w", stateDir, err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir %q: %w", stateDir, err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("chmod state dir %q: %w", stateDir, err)
	}
	_ = terminateSiblingConnectorProcesses(exePath)
	time.Sleep(300 * time.Millisecond)
	if err := rotateInstallConnectLog(stateDir); err != nil {
		return fmt.Errorf("rotate install logs: %w", err)
	}

	if strings.TrimSpace(opts.Token) == "" {
		authPath := filepath.Join(stateDir, MachineAuthPath)
		if _, err := os.Stat(authPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("missing enrollment token and %s not found in state dir (%s)", MachineAuthPath, authPath)
			}
			return fmt.Errorf("check existing auth file %s: %w", authPath, err)
		}
	}
	if err := bootstrapModelRunnerInstallEnable(); err != nil {
		fmt.Printf("warning: model runner bootstrap during install: %v\n", err)
	}
	if err := markShowLogsOnce(stateDir); err != nil {
		return fmt.Errorf("prepare one-shot log viewer marker: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	if existing, openErr := m.OpenService(serviceName); openErr == nil {
		existing.Close()
		fmt.Printf("service %q already exists, attempting automatic reconcile\n", serviceName)
		if err := uninstallWindowsService(serviceName); err != nil {
			return fmt.Errorf("service %q already exists and auto-uninstall failed: %w", serviceName, err)
		}
		time.Sleep(600 * time.Millisecond)
		if probe, probeErr := m.OpenService(serviceName); probeErr == nil {
			probe.Close()
			return fmt.Errorf("service %q still exists after auto-uninstall (possibly marked for deletion); retry install in a few seconds", serviceName)
		} else if !errors.Is(probeErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("verify service %q absence after auto-uninstall: %w", serviceName, probeErr)
		}
	} else if !errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return fmt.Errorf("open existing service %q: %w", serviceName, openErr)
	}

	cfg := mgr.Config{
		DisplayName:      "Antgpu Connect",
		Description:      "Antgpu platform connector",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}

	args := []string{"--service-run", "--service-name", serviceName, "--state-dir", stateDir}
	if strings.TrimSpace(opts.Token) != "" {
		args = append(args, strings.TrimSpace(opts.Token))
	}

	s, err := m.CreateService(serviceName, exePath, cfg, args...)
	if err != nil {
		return fmt.Errorf("create service %q: %w", serviceName, err)
	}
	defer s.Close()

	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)

	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		if !isAlreadyExistsErr(err) {
			return fmt.Errorf("install event log source: %w", err)
		}
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service %q: %w", serviceName, err)
	}

	if err := ensureTrayAutoRun(serviceName, exePath, stateDir); err != nil {
		return fmt.Errorf("configure tray autorun: %w", err)
	}
	// Do not start tray directly from installer context. When install is run
	// from a non-interactive shell, this can leave a headless tray process.
	// Service session-launch path will bring tray in the active user session.

	fmt.Printf("service %q installed and started\n", serviceName)
	return nil
}

func uninstallWindowsService(serviceName string) error {
	exePath, _ := os.Executable()
	exePath = strings.TrimSpace(exePath)

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			if exePath != "" {
				_ = terminateSiblingConnectorProcesses(exePath)
			}
			fmt.Printf("service %q is not installed\n", serviceName)
			return nil
		}
		return fmt.Errorf("open service %q: %w", serviceName, err)
	}
	defer s.Close()

	_ = stopWindowsService(serviceName, 20*time.Second)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service %q: %w", serviceName, err)
	}

	if err := removeTrayAutoRun(serviceName); err != nil {
		return fmt.Errorf("remove tray autorun: %w", err)
	}

	if err := eventlog.Remove(serviceName); err != nil && !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return fmt.Errorf("remove event log source: %w", err)
	}
	if exePath != "" {
		_ = terminateSiblingConnectorProcesses(exePath)
	}

	fmt.Printf("service %q uninstalled\n", serviceName)
	return nil
}

func defaultWindowsStateDir() string {
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	if programData == "" {
		programData = `C:\ProgramData`
	}
	return filepath.Join(programData, "Antgpu", "Connect")
}

func trayAutorunValueName(serviceName string) string {
	if serviceName == "" {
		return "AntgpuConnectTray"
	}
	return "AntgpuConnectTray-" + serviceName
}

func terminateSiblingConnectorProcesses(exePath string) error {
	base := strings.TrimSpace(filepath.Base(exePath))
	if base == "" {
		return nil
	}
	pid := os.Getpid()
	cmd := exec.Command(
		"taskkill",
		"/F",
		"/IM", base,
		"/FI", fmt.Sprintf("PID ne %d", pid),
	)
	_ = cmd.Run()
	return nil
}

func rotateInstallConnectLog(stateDir string) error {
	active := filepath.Join(stateDir, "connect.log")
	if _, err := os.Stat(active); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for i := 1; i <= 9999; i++ {
		dst := filepath.Join(stateDir, fmt.Sprintf("connect-old%d.log", i))
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(active, dst); err != nil {
			if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				fmt.Printf("warning: connect.log is in use; skipping rotation for this install\n")
				return nil
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("too many rotated logs in %s", stateDir)
}

func ensureTrayAutoRun(serviceName, exePath, stateDir string) error {
	cmd := fmt.Sprintf(`"%s" %s`, exePath, joinWindowsArgs(trayLaunchArgs(serviceName, stateDir, false)))
	valueName := trayAutorunValueName(serviceName)

	hklm, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer hklm.Close()

	if err := hklm.SetStringValue(valueName, cmd); err != nil {
		return err
	}

	// Cleanup legacy duplicate entry from older builds.
	hkcu, err := registry.OpenKey(registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err == nil {
		_ = hkcu.DeleteValue(valueName)
		hkcu.Close()
	}

	return nil
}

func removeTrayAutoRun(serviceName string) error {
	valueName := trayAutorunValueName(serviceName)
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			goto removeHKCU
		}
		return err
	}
	_ = key.DeleteValue(valueName)
	key.Close()

removeHKCU:
	ukey, uerr := registry.OpenKey(registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if uerr == nil {
		_ = ukey.DeleteValue(valueName)
		ukey.Close()
	}
	return nil
}

func bootstrapModelRunnerInstallEnable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return internaldocker.NewService().EnsureModelRunnerEnabled(ctx)
}

func trayLaunchArgs(serviceName, stateDir string, showLogs bool) []string {
	args := []string{"--service-name", serviceName, "--state-dir", stateDir}
	if showLogs {
		args = append(args, "--show-logs")
	}
	return args
}

func startTrayNow(exePath, serviceName, stateDir string, showLogs bool) error {
	cmd := exec.Command(exePath, trayLaunchArgs(serviceName, stateDir, showLogs)...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func markShowLogsOnce(stateDir string) error {
	marker := filepath.Join(stateDir, showLogsOnceMarker)
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644)
}

func consumeShowLogsOnce(stateDir string) bool {
	marker := filepath.Join(stateDir, showLogsOnceMarker)
	if _, err := os.Stat(marker); err != nil {
		return false
	}
	_ = os.Remove(marker)
	return true
}

func hasShowLogsOnce(stateDir string) bool {
	marker := filepath.Join(stateDir, showLogsOnceMarker)
	_, err := os.Stat(marker)
	return err == nil
}

func launchTrayInActiveSession(opts runtimeOptions, serviceName string, showLogs bool) error {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == 0xFFFFFFFF {
		return fmt.Errorf("no active console session")
	}
	return launchTrayInSpecificSession(opts, serviceName, sessionID, showLogs)
}

func launchTrayInSpecificSession(opts runtimeOptions, serviceName string, sessionID uint32, showLogs bool) error {
	if sessionID == 0xFFFFFFFF {
		return fmt.Errorf("invalid session id")
	}

	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return err
	}
	defer token.Close()

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		exePath, _ = os.Executable()
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = defaultWindowsStateDir()
	}
	cmdLine := fmt.Sprintf(`"%s" %s`, exePath, joinWindowsArgs(trayLaunchArgs(serviceName, stateDir, showLogs)))

	appName, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	cmdBuf, err := windows.UTF16FromString(cmdLine)
	if err != nil {
		return err
	}
	currentDir, err := windows.UTF16PtrFromString(filepath.Dir(exePath))
	if err != nil {
		return err
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return err
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Desktop = desktop
	var pi windows.ProcessInformation

	if err := windows.CreateProcessAsUser(
		token,
		appName,
		&cmdBuf[0],
		nil,
		nil,
		false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		nil,
		currentDir,
		&si,
		&pi,
	); err != nil {
		return err
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	return nil
}

func startWindowsService(serviceName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service %q: %w", serviceName, err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return fmt.Errorf("start service %q: %w", serviceName, err)
		}
	}

	fmt.Printf("service %q start requested\n", serviceName)
	return nil
}

func stopWindowsService(serviceName string, timeout time.Duration) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return fmt.Errorf("open service %q: %w", serviceName, err)
	}
	defer s.Close()

	st, err := s.Query()
	if err == nil && st.State == svc.Stopped {
		fmt.Printf("service %q already stopped\n", serviceName)
		return nil
	}

	_, ctrlErr := s.Control(svc.Stop)
	if ctrlErr != nil {
		if errors.Is(ctrlErr, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return fmt.Errorf("stop service %q: %w", serviceName, ctrlErr)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, qErr := s.Query()
		if qErr != nil {
			return fmt.Errorf("query service %q: %w", serviceName, qErr)
		}
		if state.State == svc.Stopped {
			fmt.Printf("service %q stopped\n", serviceName)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for service %q to stop", serviceName)
}

func runAsWindowsService(opts runtimeOptions, serviceName string) error {
	h := &windowsServiceHandler{
		opts:        opts,
		serviceName: serviceName,
	}
	return svc.Run(serviceName, h)
}

func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "already exists")
}

type windowsServiceHandler struct {
	opts        runtimeOptions
	serviceName string
}

func (h *windowsServiceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	// Keep CWD prepared by prepareStateWorkingDir (state-dir).
	// Switching back to exe dir breaks relative state files (.auth/conn.yaml).

	runOpts := h.opts
	runOpts.Foreground = true
	runOpts.ServiceRun = false
	runOpts.RunningAsService = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	var once sync.Once
	cancelRun := func() {
		once.Do(cancel)
	}

	go func() {
		errCh <- run(ctx, runOpts, cancelRun)
	}()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange}
	showLogs := hasShowLogsOnce(runOpts.StateDir)
	if err := launchTrayInActiveSession(runOpts, h.serviceName, showLogs); err == nil && showLogs {
		_ = consumeShowLogsOnce(runOpts.StateDir)
	}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.SessionChange:
				switch req.EventType {
				case windows.WTS_SESSION_LOGON, windows.WTS_SESSION_UNLOCK, windows.WTS_CONSOLE_CONNECT, windows.WTS_REMOTE_CONNECT:
					_ = launchTrayInSpecificSession(runOpts, h.serviceName, uint32(req.EventData), false)
				}
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancelRun()
				if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("service stop wait returned error: %v", err)
					return false, 1
				}
				return false, 0
			default:
			}
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("service run error: %v", err)
				return false, 1
			}
			return false, 0
		}
	}
}
