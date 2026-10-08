package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/antgpu/connector/internal/runtimelog"
)

var runtimeLogOutput io.Writer = os.Stdout

func setupRuntimeLog(opts runtimeOptions) (func(), error) {
	if shouldSkipRuntimeLogOpen(opts) {
		runtimeLogOutput = os.Stdout
		log.SetOutput(runtimeLogOutput)
		return func() {}, nil
	}

	if isWindowsTrayInvocation(opts) {
		// Tray UI should never lock connect.log; it reads logs via viewer only.
		runtimeLogOutput = os.Stdout
		log.SetOutput(runtimeLogOutput)
		return func() {}, nil
	}

	logPath := strings.TrimSpace(os.Getenv("CONNECT_LOG_PATH"))
	if logPath != "" {
		if err := runtimelog.RotateAndCleanup(logPath, time.Now().UTC()); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			runtimeLogOutput = os.Stdout
			log.SetOutput(runtimeLogOutput)
			log.Printf("connect: runtime log file unavailable (%s): %v", logPath, err)
			return func() {}, nil
		}
		_ = f.Chmod(0o600)
		runtimeLogOutput = buildRuntimeLogWriter(opts, f)
		log.SetOutput(runtimeLogOutput)
		restore := redirectServiceStdPipes(opts, f)
		return func() {
			restore()
			_ = f.Close()
		}, nil
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		if err := runtimelog.RotateAndCleanup("connect.log", time.Now().UTC()); err != nil {
			return nil, err
		}
		f, err := os.OpenFile("connect.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			runtimeLogOutput = os.Stdout
			log.SetOutput(runtimeLogOutput)
			log.Printf("connect: runtime log file unavailable (%s): %v", "connect.log", err)
			return func() {}, nil
		}
		_ = f.Chmod(0o600)
		runtimeLogOutput = buildRuntimeLogWriter(opts, f)
		log.SetOutput(runtimeLogOutput)
		restore := redirectServiceStdPipes(opts, f)
		return func() {
			restore()
			_ = f.Close()
		}, nil
	}

	primaryPath := filepath.Join(stateDir, "connect.log")
	if err := runtimelog.RotateAndCleanup(primaryPath, time.Now().UTC()); err != nil {
		return nil, err
	}
	primaryFile, primaryErr := os.OpenFile(primaryPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if primaryErr != nil {
		runtimeLogOutput = os.Stdout
		log.SetOutput(runtimeLogOutput)
		log.Printf("connect: runtime log file unavailable (%s): %v", primaryPath, primaryErr)
		return func() {}, nil
	}

	_ = primaryFile.Chmod(0o600)
	runtimeLogOutput = buildRuntimeLogWriter(opts, primaryFile)
	log.SetOutput(runtimeLogOutput)
	restore := redirectServiceStdPipes(opts, primaryFile)
	return func() {
		restore()
		_ = primaryFile.Close()
	}, nil
}

func isWindowsTrayInvocation(opts runtimeOptions) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if strings.TrimSpace(opts.Token) != "" {
		return false
	}
	if opts.Foreground || opts.PrintVersion {
		return false
	}
	if opts.ServiceInstall || opts.ServiceUninstall || opts.ServiceStart || opts.ServiceStop || opts.ServiceRun {
		return false
	}
	return true
}

func shouldSkipRuntimeLogOpen(opts runtimeOptions) bool {
	return opts.ServiceInstall || opts.ServiceUninstall || opts.ServiceStart || opts.ServiceStop
}

func buildRuntimeLogWriter(opts runtimeOptions, fileWriter io.Writer) io.Writer {
	if fileWriter == nil {
		return os.Stdout
	}
	// Services and detached tray processes often have invalid stdout handles.
	if opts.ServiceRun || !isStdoutUsable() {
		return fileWriter
	}
	return io.MultiWriter(os.Stdout, fileWriter)
}

func isStdoutUsable() bool {
	if os.Stdout == nil {
		return false
	}
	_, err := os.Stdout.Stat()
	return err == nil
}

func redirectServiceStdPipes(opts runtimeOptions, target *os.File) func() {
	if !opts.ServiceRun || target == nil {
		return func() {}
	}
	prevOut := os.Stdout
	prevErr := os.Stderr
	os.Stdout = target
	os.Stderr = target
	return func() {
		os.Stdout = prevOut
		os.Stderr = prevErr
	}
}
