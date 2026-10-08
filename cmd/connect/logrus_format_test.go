package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestStdLikeLogrusFormatterOmitsLevelLabels(t *testing.T) {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.Local)
	entry := &logrus.Entry{
		Time:    ts,
		Level:   logrus.InfoLevel,
		Message: "hello",
		Data: logrus.Fields{
			"z": "two",
			"a": 1,
		},
	}

	gotBytes, err := newStdLikeLogrusFormatter().Format(entry)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	got := string(gotBytes)
	want := "2026/06/08 12:00:00 hello a=1 z=two\n"
	if got != want {
		t.Fatalf("unexpected formatted line:\nwant %q\n got %q", want, got)
	}
	for _, disallowed := range []string{"[info]", "level=", "time=", "msg="} {
		if strings.Contains(got, disallowed) {
			t.Fatalf("formatted line contains %q: %q", disallowed, got)
		}
	}
}

func TestNormalizeLogrusTextLineOmitsLevelLabels(t *testing.T) {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.Local)
	input := `time="` + ts.Format(time.RFC3339Nano) + `" level=info msg="hello world" component=tunnel`

	got := normalizeLogrusTextLine(input)
	want := "2026/06/08 12:00:00 hello world component=tunnel"
	if got != want {
		t.Fatalf("unexpected normalized line:\nwant %q\n got %q", want, got)
	}
	for _, disallowed := range []string{"[info]", "level=", "time=", "msg="} {
		if strings.Contains(got, disallowed) {
			t.Fatalf("normalized line contains %q: %q", disallowed, got)
		}
	}
}

func TestNormalizeLogrusTextLineLeavesPlainLines(t *testing.T) {
	line := "2026/06/08 12:00:00 already formatted"
	if got := normalizeLogrusTextLine(line); got != line {
		t.Fatalf("unexpected normalized plain line: %q", got)
	}
}
