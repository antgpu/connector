package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type runtimeOptions struct {
	Foreground       bool
	PrintVersion     bool
	Token            string
	ServiceName      string
	StateDir         string
	ShowLogs         bool
	TestAutoUpdate   bool
	RunningAsService bool

	ServiceInstall   bool
	ServiceUninstall bool
	ServiceStart     bool
	ServiceStop      bool
	ServiceRun       bool
}

func parseRuntimeOptions(args []string) (runtimeOptions, error) {
	var out runtimeOptions

	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.BoolVar(&out.Foreground, "foreground", false, "run in foreground (disable daemon detach)")
	fs.BoolVar(&out.PrintVersion, "version", false, "print version and exit")
	fs.StringVar(&out.ServiceName, "service-name", "antgpu-connect", "windows service name")
	fs.BoolVar(&out.ServiceInstall, "service-install", false, "install Windows service")
	fs.BoolVar(&out.ServiceUninstall, "service-uninstall", false, "uninstall Windows service")
	fs.BoolVar(&out.ServiceStart, "service-start", false, "start Windows service")
	fs.BoolVar(&out.ServiceStop, "service-stop", false, "stop Windows service")
	fs.BoolVar(&out.ServiceRun, "service-run", false, "internal: run under Windows Service Control Manager")
	fs.StringVar(&out.StateDir, "state-dir", "", "connector state working directory")
	fs.BoolVar(&out.ShowLogs, "show-logs", false, "internal: open tray log viewer on startup")
	fs.BoolVar(&out.TestAutoUpdate, "test-auto-update", false, "force connector update check at startup (dev/testing)")
	fs.SetOutput(os.Stderr)

	if err := fs.Parse(args); err != nil {
		return out, err
	}

	if fs.NArg() > 0 {
		out.Token = strings.TrimSpace(fs.Arg(0))
	}
	if fs.NArg() > 1 {
		return out, fmt.Errorf("unexpected extra arguments")
	}

	return out, nil
}

func shouldDaemonize(opts runtimeOptions) bool {
	if opts.Foreground {
		return false
	}
	if strings.TrimSpace(os.Getenv("SWARMIND_CONNECT_FOREGROUND")) == "1" {
		return false
	}
	return true
}

func startDaemonChild(opts runtimeOptions) error {
	logPath := strings.TrimSpace(os.Getenv("CONNECT_LOG_PATH"))
	if logPath == "" {
		logPath = "connect.log"
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log file: %w", err)
	}
	_ = logFile.Chmod(0o600)
	defer logFile.Close()

	childArgs := []string{"--foreground"}
	if opts.Token != "" {
		childArgs = append(childArgs, opts.Token)
	}

	cmd := exec.Command(os.Args[0], childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "SWARMIND_CONNECT_FOREGROUND=1")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon child: %w", err)
	}
	_ = cmd.Process.Release()

	fmt.Printf("connect daemon started pid=%d log=%s\n", cmd.Process.Pid, logPath)
	return nil
}

func replacementLaunchArgs(args []string) []string {
	if len(args) == 0 {
		return []string{"--foreground"}
	}
	out := append([]string(nil), args...)
	return out
}

func spawnReplacementBinary(binaryPath string) error {
	env := append(os.Environ(), "SWARMIND_CONNECT_FOREGROUND=1")
	args := replacementLaunchArgs(os.Args[1:])

	// Under systemd/foreground, replace current process in-place so PID owner
	// does not spawn a second connector instance that races on Nebula UDP bind.
	if strings.TrimSpace(os.Getenv("SWARMIND_CONNECT_FOREGROUND")) == "1" ||
		strings.TrimSpace(os.Getenv("INVOCATION_ID")) != "" {
		execArgs := append([]string{binaryPath}, args...)
		if err := syscall.Exec(binaryPath, execArgs, env); err != nil {
			return fmt.Errorf("exec replacement binary: %w", err)
		}
		return nil
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start replacement binary: %w", err)
	}

	go func() {
		time.Sleep(700 * time.Millisecond)
		os.Exit(0)
	}()

	return nil
}
