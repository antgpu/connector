package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func TestWorkloadStartRefusesLegacyAndAutomaticRestart(t *testing.T) {
	for _, tc := range []struct {
		name string
		host container.HostConfig
	}{
		{"legacy bridge", container.HostConfig{NetworkMode: "bridge"}},
		{"automatic restart", container.HostConfig{NetworkMode: workloadNetwork, Sysctls: map[string]string{"net.ipv6.conf.all.disable_ipv6": "1"}, RestartPolicy: container.RestartPolicy{Name: "always"}}},
		{"IPv6 enabled", container.HostConfig{NetworkMode: workloadNetwork}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/json") {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": strings.Repeat("a", 64), "Config": map[string]any{"Labels": map[string]string{"managed-by": "template-manager"}}, "HostConfig": tc.host})
					return
				}
				mutations++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			if err := StartContainer(context.Background(), cli, strings.Repeat("a", 64)); err == nil {
				t.Fatal("unsafe workload started")
			}
			if mutations != 0 {
				t.Fatal("unsafe workload mutated Docker before failing closed")
			}
		})
	}
}
