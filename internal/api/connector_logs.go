package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/antgpu/connector/internal/runtimelog"
	"github.com/swaggest/usecase/status"
)

type ConnectorLogsInput struct {
	Since string `query:"since" description:"RFC3339 lower bound for log timestamps"`
	Tail  int    `query:"tail" description:"Max matching lines to return; use -1 for unlimited"`
	Limit int    `query:"limit" description:"Max response bytes"`
}

type ConnectorLogsOutput struct {
	Content   string `json:"content"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
}

func (s APIService) ConnectorLogs(ctx context.Context, input ConnectorLogsInput, output *ConnectorLogsOutput) error {
	var since time.Time
	if strings.TrimSpace(input.Since) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(input.Since))
		if err != nil {
			return status.Wrap(fmt.Errorf("since must be RFC3339"), status.InvalidArgument)
		}
		since = parsed.UTC()
	}

	if input.Tail == 0 {
		input.Tail = runtimelog.DefaultTailLines
	}
	if input.Limit <= 0 {
		input.Limit = runtimelog.DefaultReadLimit
	}

	primaryPath := runtimelog.ResolvePrimaryPath(s.StateDir)
	if err := runtimelog.RotateAndCleanup(primaryPath, time.Now().UTC()); err != nil {
		return status.Wrap(fmt.Errorf("prepare logs: %w", err), status.Internal)
	}
	result, err := runtimelog.Read(primaryPath, runtimelog.ReadOptions{
		Since: since,
		Tail:  input.Tail,
		Limit: input.Limit,
	})
	if err != nil {
		return status.Wrap(fmt.Errorf("read logs: %w", err), status.Internal)
	}

	output.Content = result.Content
	output.Lines = result.Lines
	output.Truncated = result.Truncated
	return nil
}
