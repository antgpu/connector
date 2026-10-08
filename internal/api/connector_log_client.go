package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ConnectorLogInput struct {
	Level            string         `json:"level,omitempty"`
	Phase            string         `json:"phase,omitempty"`
	Message          string         `json:"message"`
	Fields           map[string]any `json:"fields,omitempty"`
	ConnectorVersion string         `json:"connector_version,omitempty"`
}

func (s APIService) SendConnectorLog(ctx context.Context, token string, entry ConnectorLogInput) error {
	token = strings.TrimSpace(token)
	entry.Message = strings.TrimSpace(entry.Message)
	if token == "" || entry.Message == "" {
		return nil
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	if entry.Phase == "" {
		entry.Phase = "connector"
	}
	if len(entry.Message) > 4000 {
		entry.Message = entry.Message[:4000]
	}

	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.APIAddr, "/")+"/machines/connector/logs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("non-OK status code: %d", resp.StatusCode)
	}
	return nil
}
