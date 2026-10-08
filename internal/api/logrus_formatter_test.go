package api

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestConnectorStdLikeLogrusFormatterOmitsLevelLabels(t *testing.T) {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.Local)
	entry := &logrus.Entry{
		Time:    ts,
		Level:   logrus.WarnLevel,
		Message: "hello",
		Data: logrus.Fields{
			"z": "two",
			"a": 1,
		},
	}

	gotBytes, err := (&connectorStdLikeLogrusFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	got := string(gotBytes)
	want := "2026/06/08 12:00:00 hello a=1 z=two\n"
	if got != want {
		t.Fatalf("unexpected formatted line:\nwant %q\n got %q", want, got)
	}
	for _, disallowed := range []string{"[warning]", "[warn]", "level=", "time=", "msg="} {
		if strings.Contains(got, disallowed) {
			t.Fatalf("formatted line contains %q: %q", disallowed, got)
		}
	}
}
