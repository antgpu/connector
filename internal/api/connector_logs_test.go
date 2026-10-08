package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectorLogs_ReadsWithToken(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "connect.log")
	if err := os.WriteFile(logPath, []byte("2026/06/08 12:00:00 hello\n2026/06/08 12:01:00 world\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	svc := APIService{StateDir: dir, MachineAuthData: MachineAuthData{MachineToken: "secret"}}
	out := &ConnectorLogsOutput{}
	if err := svc.ConnectorLogs(context.Background(), ConnectorLogsInput{Tail: 10, Limit: 4096}, out); err != nil {
		t.Fatalf("ConnectorLogs returned error: %v", err)
	}
	if out.Lines != 2 || out.Content == "" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestConnectorLogs_ReadsUnlimitedTail(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "connect.log")
	if err := os.WriteFile(logPath, []byte("2026/06/08 12:00:00 hello\n2026/06/08 12:01:00 world\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	svc := APIService{StateDir: dir, MachineAuthData: MachineAuthData{MachineToken: "secret"}}
	out := &ConnectorLogsOutput{}
	if err := svc.ConnectorLogs(context.Background(), ConnectorLogsInput{Tail: -1, Limit: 4096}, out); err != nil {
		t.Fatalf("ConnectorLogs returned error: %v", err)
	}
	if out.Lines != 2 || out.Content == "" {
		t.Fatalf("unexpected output: %+v", out)
	}
}
