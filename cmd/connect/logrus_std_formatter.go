package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

type stdLikeLogrusFormatter struct{}

func newStdLikeLogrusFormatter() logrus.Formatter {
	return &stdLikeLogrusFormatter{}
}

func (f *stdLikeLogrusFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b bytes.Buffer
	ts := entry.Time.Local().Format("2006/01/02 15:04:05")
	msg := strings.TrimSpace(entry.Message)
	if msg == "" {
		msg = "-"
	}
	_, _ = fmt.Fprintf(&b, "%s %s", ts, msg)

	if len(entry.Data) > 0 {
		keys := make([]string, 0, len(entry.Data))
		for k := range entry.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(&b, " %s=%v", k, entry.Data[k])
		}
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
