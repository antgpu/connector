package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/antgpu/connector/internal/hw"
)

var ErrBenchmarkUnsupported = errors.New("backend does not support benchmark policy")

type BenchmarkIdentity struct {
	GPU     string `json:"gpu"`
	Network string `json:"network"`
}
type BenchmarkGroup struct {
	Run           bool       `json:"run"`
	Reason        string     `json:"reason"`
	Status        string     `json:"status"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	NextDueAt     *time.Time `json:"next_due_at,omitempty"`
}
type BenchmarkPolicy struct {
	Enabled    bool                      `json:"enabled"`
	Trusted    bool                      `json:"trusted"`
	Ready      bool                      `json:"ready"`
	Deferred   bool                      `json:"deferred"`
	Reason     string                    `json:"reason,omitempty"`
	LeaseID    string                    `json:"lease_id,omitempty"`
	LeaseUntil *time.Time                `json:"lease_until,omitempty"`
	Groups     map[string]BenchmarkGroup `json:"groups"`
}

func (s APIService) benchmarkRequest(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.APIAddr+"/machines/benchmarks/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.MachineAuthData.MachineToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return ErrBenchmarkUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("benchmark %s returned HTTP %d", path, resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(output)
	}
	return nil
}
func (s APIService) BenchmarkPlan(ctx context.Context, identity BenchmarkIdentity) (BenchmarkPolicy, error) {
	var out BenchmarkPolicy
	err := s.benchmarkRequest(ctx, "plan", map[string]any{"identity": identity}, &out)
	return out, err
}
func (s APIService) BenchmarkRenew(ctx context.Context, lease string) error {
	return s.benchmarkRequest(ctx, "renew", map[string]string{"lease_id": lease}, nil)
}
func (s APIService) BenchmarkReadinessFailure(ctx context.Context, message string) error {
	return s.benchmarkRequest(ctx, "failure", map[string]string{"error": message}, nil)
}
func (s APIService) BenchmarkComplete(ctx context.Context, lease string, specs *hw.SystemSpecs, message string) (BenchmarkPolicy, error) {
	var out BenchmarkPolicy
	err := s.benchmarkRequest(ctx, "complete", map[string]any{"lease_id": lease, "specs": specs, "error": message}, &out)
	return out, err
}
