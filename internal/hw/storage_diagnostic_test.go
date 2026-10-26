package hw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

func TestStorageDiagnosticRejectsInvalidCounts(t *testing.T) {
	for _, raw := range []string{"", "4096 100 10", "4096 100 10 11", "4096 100 101 10", "-1 100 10 5", "0 100 10 5", "4096 18446744073709551615 1 1", "4096 100 10 5 extra"} {
		if _, err := parseStorageDiagnosticStat(raw); err == nil {
			t.Errorf("accepted invalid counts %q", raw)
		}
	}
	got, err := parseStorageDiagnosticStat("4096 100 10 5\n")
	if err != nil || got["total_bytes"] != 409600 || got["available_bytes"] != 20480 {
		t.Fatalf("unexpected counts: %v %v", got, err)
	}
}

func TestStorageDiagnosticHelperConstrainedAndRemoved(t *testing.T) {
	for _, scenario := range []string{"success", "start_failure", "bad_counts", "wrong_digest", "uncached", "pull"} {
		t.Run(scenario, func(t *testing.T) {
			created, removed := false, false
			inspects, pulls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
					inspects++
					if scenario == "uncached" || (scenario == "pull" && inspects == 1) {
						http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
						return
					}
					digest := storageDiagnosticImage
					if scenario == "wrong_digest" {
						digest = "untrusted:latest@sha256:other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "RepoDigests": []string{digest}})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/images/create"):
					pulls++
					if scenario != "pull" || r.URL.Query().Get("fromImage") != "docker.io/library/alpine" || !strings.Contains(r.URL.Query().Encode(), "14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce") {
						t.Errorf("unexpected image pull: %s", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte("{\"status\":\"downloaded\"}\n"))
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/containers/create"):
					var body struct {
						container.Config
						HostConfig container.HostConfig
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					h := body.HostConfig
					if !h.ReadonlyRootfs || h.Privileged || h.NetworkMode != "none" || len(h.Mounts) != 0 || len(h.Binds) != 0 || len(h.CapAdd) != 0 || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || body.User != "65534:65534" || h.Memory != 32<<20 || h.PidsLimit == nil || *h.PidsLimit != 16 || len(h.SecurityOpt) != 1 || h.SecurityOpt[0] != "no-new-privileges:true" {
						t.Errorf("unsafe diagnostic helper: %+v", h)
					}
					created = true
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"Id":"owned-probe"}`))
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/owned-probe/start"):
					if scenario == "start_failure" {
						http.Error(w, `{"message":"fixture failure"}`, http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/owned-probe/wait"):
					_, _ = w.Write([]byte(`{"StatusCode":0}`))
				case strings.HasSuffix(r.URL.Path, "/owned-probe/logs"):
					raw := "4096 100 10 5\n"
					if scenario == "bad_counts" {
						raw = "4096 100 10 11\n"
					}
					_, _ = stdcopy.NewStdWriter(w, stdcopy.Stdout).Write([]byte(raw))
				case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/containers/owned-probe"):
					removed = true
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker mutation/request: %s %s", r.Method, r.URL.Path)
					http.Error(w, `{"message":"unexpected request"}`, http.StatusBadRequest)
				}
			}))
			defer server.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			got := probeStorageDiagnosticGuest(context.Background(), cli, scenario == "pull")
			if created != removed {
				t.Fatalf("leaked probe: created=%t removed=%t", created, removed)
			}
			if (scenario == "success" || scenario == "pull") && (got["error"] != nil || got["available_bytes"] != uint64(20480)) {
				t.Fatalf("unexpected success result: %v", got)
			}
			if scenario != "success" && scenario != "pull" && got["error"] == nil {
				t.Fatalf("failure went unreported: %v", got)
			}
			if (scenario == "wrong_digest" || scenario == "uncached") && created {
				t.Fatal("created a container without the cached pinned image")
			}
			if scenario == "pull" && (pulls != 1 || inspects != 2) {
				t.Fatal("did not inspect the pinned artifact after downloading")
			}
		})
	}
}

func TestStorageDiagnosticGuestIntegration(t *testing.T) {
	if os.Getenv("RUN_STORAGE_DIAGNOSTIC_INTEGRATION") != "1" {
		t.Skip("requires local disposable Docker")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	got := probeStorageDiagnosticGuest(context.Background(), cli, false)
	if got["error"] != nil || got["cleanup_error"] != nil || got["total_bytes"] == uint64(0) {
		t.Fatalf("real guest probe failed: %v", got)
	}
	t.Logf("guest storage counts: %v", got)
}
