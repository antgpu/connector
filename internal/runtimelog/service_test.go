package runtimelog

import "testing"

func TestParseLogTimestampMinimalDatePrefix(t *testing.T) {
	ts, ok := parseLogTimestamp("2026/06/08 12:00:00 hello")
	if !ok {
		t.Fatal("expected timestamp to parse")
	}
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}
