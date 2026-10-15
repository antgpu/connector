package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antgpu/connector/internal/api"
	"github.com/antgpu/connector/internal/hw"
)

func TestBenchmarkCycleCachingAndIdleAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		ready, deferred, running bool
		due                      string
		wantRuns, wantQuick      int
		wantReady                bool
	}{
		{name: "cached reboot", ready: true, wantQuick: 1, wantReady: true},
		{name: "network renewal", due: "network", wantRuns: 1, wantReady: true},
		{name: "stress renewal", due: "stress", wantRuns: 1, wantReady: true},
		{name: "active rental", due: "network", deferred: true, wantReady: true},
		{name: "local workload", due: "stress", running: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := map[string]api.BenchmarkGroup{}
			for _, name := range []string{"gpu_base", "compat", "stress", "network"} {
				groups[name] = api.BenchmarkGroup{Run: tc.due == name}
			}
			var completed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing machine authentication")
					w.WriteHeader(403)
					return
				}
				switch r.URL.Path {
				case "/machines/benchmarks/plan":
					p := api.BenchmarkPolicy{Enabled: true, Ready: tc.ready, Deferred: tc.deferred, Reason: "active_rental", Groups: groups}
					if !tc.ready && !tc.deferred {
						p.LeaseID = "fixture-reservation"
					}
					_ = json.NewEncoder(w).Encode(p)
				case "/machines/config":
					fmt.Fprint(w, `{"bench":{"iperf_host":"test.example","iperf_seconds":2}}`)
				case "/machines/benchmarks/complete":
					completed.Add(1)
					var input struct {
						LeaseID string          `json:"lease_id"`
						Specs   *hw.SystemSpecs `json:"specs"`
					}
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input.LeaseID != "fixture-reservation" {
						t.Error("wrong reservation")
					}
					if input.Specs != nil && (input.Specs.TotalMeasuredTFLOPS != 0 || len(input.Specs.GPUWorkload) > 0) {
						t.Error("unrequested performance data was published as fresh")
					}
					if tc.running && input.Specs != nil {
						t.Error("benchmarked over local workload")
					}
					_ = json.NewEncoder(w).Encode(api.BenchmarkPolicy{Ready: !tc.running})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			s := api.APIService{APIAddr: server.URL, MachineAuthData: api.MachineAuthData{MachineToken: "fixture-token"}, WorkloadMutex: &sync.RWMutex{}, BenchmarkReady: &atomic.Bool{}}
			runs, quick := 0, 0
			checks := benchmarkHostChecks{
				identity: func(context.Context, *hw.SystemSpecs) (string, string, error) {
					return strings.Repeat("a", 64), strings.Repeat("b", 64), nil
				},
				running:   func(context.Context) (bool, error) { return tc.running, nil },
				readiness: func(context.Context) error { quick++; return nil },
				run: func(_ context.Context, specs *hw.SystemSpecs, _ hw.BenchNetworkConfig, mode hw.BenchRunMode) {
					runs++
					specs.TotalMeasuredTFLOPS = 999
					specs.GPUWorkload = map[string]any{"status": "success"}
					if s.WorkloadMutex.TryRLock() {
						s.WorkloadMutex.RUnlock()
						t.Error("provisioning was not excluded")
					}
					if (mode.RunNetwork != (tc.due == "network")) || mode.RunGPUBase || mode.RunCompat || (mode.RunStress != (tc.due == "stress")) {
						t.Errorf("wrong selective mode: %+v", mode)
					}
				},
				modelRunner: func(context.Context) *hw.ModelRunnerCapability { return nil }, renewEvery: time.Second,
			}
			managed, err := runBenchmarkCycleWithChecks(context.Background(), &s, &hw.SystemSpecs{}, true, checks)
			if err != nil || !managed || runs != tc.wantRuns || quick != tc.wantQuick || s.BenchmarkReady.Load() != tc.wantReady {
				t.Fatalf("managed=%t err=%v runs=%d quick=%d ready=%t", managed, err, runs, quick, s.BenchmarkReady.Load())
			}
			if (tc.ready || tc.deferred) && completed.Load() != 0 {
				t.Fatal("cached/deferred results were republished")
			}
		})
	}
}

func TestBenchmarkCycleLosingReservationCancelsWork(t *testing.T) {
	var renewals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/machines/benchmarks/plan":
			fmt.Fprint(w, `{"enabled":true,"lease_id":"lease","groups":{"gpu_base":{"run":true},"compat":{"run":true},"stress":{"run":true},"network":{"run":true}}}`)
		case "/machines/config":
			fmt.Fprint(w, `{"bench":{}}`)
		case "/machines/benchmarks/renew":
			renewals.Add(1)
			w.WriteHeader(409)
		case "/machines/benchmarks/complete":
			var input struct {
				Specs *hw.SystemSpecs `json:"specs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Specs != nil {
				t.Error("cancelled run published successful data")
			}
			fmt.Fprint(w, `{"ready":false}`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	s := api.APIService{APIAddr: server.URL, WorkloadMutex: &sync.RWMutex{}, BenchmarkReady: &atomic.Bool{}}
	checks := benchmarkHostChecks{identity: func(context.Context, *hw.SystemSpecs) (string, string, error) { return "gpu", "network", nil }, running: func(context.Context) (bool, error) { return false, nil }, run: func(ctx context.Context, _ *hw.SystemSpecs, _ hw.BenchNetworkConfig, _ hw.BenchRunMode) { <-ctx.Done() }, modelRunner: func(context.Context) *hw.ModelRunnerCapability { return nil }, renewEvery: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := runBenchmarkCycleWithChecks(ctx, &s, &hw.SystemSpecs{}, false, checks)
	if err == nil || renewals.Load() == 0 || s.BenchmarkReady.Load() {
		t.Fatalf("lost reservation was not enforced: %v", err)
	}
}

func TestRejectIncompleteBenchmarkPolicy(t *testing.T) {
	if validateBenchmarkPolicy(api.BenchmarkPolicy{Enabled: true, Ready: true}) == nil {
		t.Fatal("empty policy allowed a readiness bypass")
	}
}

func TestBenchmarkIdentityFailureInvalidatesBackendApproval(t *testing.T) {
	var invalidations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/machines/benchmarks/failure" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		invalidations.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	ready := &atomic.Bool{}
	ready.Store(true)
	s := api.APIService{APIAddr: server.URL, WorkloadMutex: &sync.RWMutex{}, BenchmarkReady: ready}
	checks := benchmarkHostChecks{identity: func(context.Context, *hw.SystemSpecs) (string, string, error) {
		return "", "", fmt.Errorf("NVIDIA driver unavailable")
	}}
	_, err := runBenchmarkCycleWithChecks(context.Background(), &s, &hw.SystemSpecs{}, true, checks)
	if err == nil || ready.Load() || invalidations.Load() != 1 {
		t.Fatalf("failed readiness retained cached approval: %v", err)
	}
}
