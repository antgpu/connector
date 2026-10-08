package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
)

func TestWorkloadStorageUsesAvailableQuotaSupport(t *testing.T) {
	for _, tc := range []struct {
		name, driver, backing, wantSize string
		diskMB                          int
		probeFailure                    bool
	}{
		{name: "containerd overlayfs", driver: "overlayfs", diskMB: 1024},
		{name: "overlay2 ext4", driver: "overlay2", backing: "extfs", diskMB: 1024},
		{name: "vfs", driver: "vfs", diskMB: 1024},
		{name: "btrfs quota", driver: "btrfs", diskMB: 1024, wantSize: "1024m"},
		{name: "zfs default quota", driver: "zfs", wantSize: "8192m"},
		{name: "unavailable quota probe", probeFailure: true, diskMB: 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.probeFailure {
					http.Error(w, "unavailable", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(system.Info{Driver: tc.driver, DriverStatus: [][2]string{{"Backing Filesystem", tc.backing}}})
			}))
			defer server.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			host := &container.HostConfig{}
			if err := configureWorkloadStorage(context.Background(), cli, host, tc.diskMB); err != nil {
				t.Fatalf("storage setup must not prevent provisioning: %v", err)
			}
			if got := host.StorageOpt["size"]; got != tc.wantSize {
				t.Fatalf("size = %q, want %q", got, tc.wantSize)
			}
		})
	}
}
