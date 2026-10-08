package runtimelog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultTailLines    = 400
	DefaultReadLimit    = 256 * 1024
	DefaultRotateSize   = 20 * 1024 * 1024
	DefaultRotateAge    = 24 * time.Hour
	DefaultRetentionAge = 8 * 24 * time.Hour
)

type ReadOptions struct {
	Since time.Time
	Tail  int
	Limit int
}

type ReadResult struct {
	Content   string
	Lines     int
	Truncated bool
}

func ResolvePrimaryPath(stateDir string) string {
	if envPath := strings.TrimSpace(os.Getenv("CONNECT_LOG_PATH")); envPath != "" {
		return envPath
	}
	if strings.TrimSpace(stateDir) == "" {
		return "connect.log"
	}
	return filepath.Join(stateDir, "connect.log")
}

func RotateAndCleanup(primaryPath string, now time.Time) error {
	if strings.TrimSpace(primaryPath) == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if filepath.Dir(primaryPath) != "." {
		if err := os.MkdirAll(filepath.Dir(primaryPath), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(primaryPath), 0o700); err != nil {
			return err
		}
	}
	if err := rotateIfNeeded(primaryPath, now); err != nil {
		return err
	}
	return cleanupOldLogs(primaryPath, now)
}

func Read(primaryPath string, opts ReadOptions) (ReadResult, error) {
	files, err := listLogFiles(primaryPath)
	if err != nil {
		return ReadResult{}, err
	}
	if len(files) == 0 {
		return ReadResult{}, nil
	}
	initialCap := opts.Tail
	if initialCap < 0 {
		initialCap = 0
	}
	lines := make([]string, 0, initialCap)
	includeUntimed := opts.Since.IsZero()
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if ts, ok := parseLogTimestamp(line); ok {
				includeUntimed = opts.Since.IsZero() || !ts.Before(opts.Since)
			}
			if !includeUntimed {
				continue
			}
			lines = append(lines, line)
			if opts.Tail > 0 && len(lines) > opts.Tail {
				lines = append([]string(nil), lines[len(lines)-opts.Tail:]...)
			}
		}
		_ = f.Close()
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	truncated := false
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	if len(content) > limit {
		truncated = true
		content = content[len(content)-limit:]
		if idx := strings.IndexByte(content, '\n'); idx >= 0 && idx+1 < len(content) {
			content = content[idx+1:]
		}
	}
	return ReadResult{Content: content, Lines: len(lines), Truncated: truncated}, nil
}

func rotateIfNeeded(primaryPath string, now time.Time) error {
	info, err := os.Stat(primaryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() < DefaultRotateSize && now.Sub(info.ModTime()) < DefaultRotateAge {
		return nil
	}
	rotatedPath := filepath.Join(filepath.Dir(primaryPath), fmt.Sprintf("connect-%s.log", now.UTC().Format("20060102-150405")))
	if err := os.Rename(primaryPath, rotatedPath); err != nil {
		return err
	}
	return nil
}

func cleanupOldLogs(primaryPath string, now time.Time) error {
	files, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "connect*.log"))
	if err != nil {
		return err
	}
	cutoff := now.Add(-DefaultRetentionAge)
	for _, path := range files {
		if path == primaryPath {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
	return nil
}

func listLogFiles(primaryPath string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "connect*.log"))
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i] == primaryPath {
			return false
		}
		if matches[j] == primaryPath {
			return true
		}
		infoI, errI := os.Stat(matches[i])
		infoJ, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return matches[i] < matches[j]
		}
		return infoI.ModTime().Before(infoJ.ModTime())
	})
	return matches, nil
}

func parseLogTimestamp(line string) (time.Time, bool) {
	if len(line) < len("2006/01/02 15:04:05") {
		return time.Time{}, false
	}
	ts, err := time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return ts.UTC(), true
}
