package main

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var logrusTextLineRE = regexp.MustCompile(`^time="([^"]+)" level=([a-zA-Z]+) msg=(?:"((?:[^"\\]|\\.)*)"|([^ ]+))(?: (.*))?$`)

type logrusNormalizingWriter struct {
	dst io.Writer

	mu  sync.Mutex
	buf bytes.Buffer
}

func newLogrusNormalizingWriter(dst io.Writer) io.Writer {
	if dst == nil {
		return nil
	}
	return &logrusNormalizingWriter{dst: dst}
}

func (w *logrusNormalizingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.buf.Write(p); err != nil {
		return 0, err
	}

	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}

		line := string(data[:idx])
		_ = w.buf.Next(idx + 1)

		out := normalizeLogrusTextLine(line) + "\n"
		if _, err := io.WriteString(w.dst, out); err != nil {
			return len(p), err
		}
	}

	return len(p), nil
}

func normalizeLogrusTextLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, `time="`) || !strings.Contains(line, " level=") || !strings.Contains(line, " msg=") {
		return line
	}

	m := logrusTextLineRE.FindStringSubmatch(line)
	if len(m) != 6 {
		return line
	}
	tsRaw := strings.TrimSpace(m[1])
	msgEscaped := m[3]
	msgBare := strings.TrimSpace(m[4])
	rest := strings.TrimSpace(m[5])

	t, err := time.Parse(time.RFC3339Nano, tsRaw)
	if err != nil {
		return line
	}
	ts := t.Local().Format("2006/01/02 15:04:05")

	msg := msgBare
	if msgEscaped != "" {
		if unquoted, qErr := strconv.Unquote(`"` + msgEscaped + `"`); qErr == nil {
			msg = unquoted
		} else {
			msg = msgEscaped
		}
	}
	if msg == "" {
		msg = "-"
	}

	if rest != "" {
		return fmt.Sprintf("%s %s %s", ts, msg, rest)
	}
	return fmt.Sprintf("%s %s", ts, msg)
}
