package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

func TestPickContainerIP(t *testing.T) {
	insp := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			DefaultNetworkSettings: container.DefaultNetworkSettings{IPAddress: "172.17.0.9"},
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.18.0.2"},
			},
		},
	}

	if got := pickContainerIP(insp); got != "172.18.0.2" {
		t.Fatalf("unexpected container IP: got=%q", got)
	}
}

func TestPickPublishedSSHHostEndpoint(t *testing.T) {
	testCases := []struct {
		name     string
		binding  nat.PortBinding
		wantHost string
		wantPort int
	}{
		{
			name:     "wildcard-ipv4-uses-loopback",
			binding:  nat.PortBinding{HostIP: "0.0.0.0", HostPort: "22022"},
			wantHost: "127.0.0.1",
			wantPort: 22022,
		},
		{
			name:     "empty-host-uses-loopback",
			binding:  nat.PortBinding{HostPort: "22023"},
			wantHost: "127.0.0.1",
			wantPort: 22023,
		},
		{
			name:     "specific-host-preserved",
			binding:  nat.PortBinding{HostIP: "192.0.2.10", HostPort: "22024"},
			wantHost: "192.0.2.10",
			wantPort: 22024,
		},
		{
			name:     "wildcard-ipv6-uses-loopback",
			binding:  nat.PortBinding{HostIP: "::", HostPort: "22025"},
			wantHost: "::1",
			wantPort: 22025,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			insp := container.InspectResponse{
				NetworkSettings: &container.NetworkSettings{
					NetworkSettingsBase: container.NetworkSettingsBase{
						Ports: nat.PortMap{
							"22/tcp": []nat.PortBinding{tc.binding},
						},
					},
				},
			}

			host, port := pickPublishedSSHHostEndpoint(insp)
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("unexpected endpoint: got=%s:%d want=%s:%d", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestPickPublishedSSHHostEndpointSkipsInvalidBindings(t *testing.T) {
	insp := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			NetworkSettingsBase: container.NetworkSettingsBase{
				Ports: nat.PortMap{
					"22/tcp": []nat.PortBinding{
						{HostIP: "0.0.0.0", HostPort: ""},
						{HostIP: "0.0.0.0", HostPort: "bad"},
						{HostIP: "0.0.0.0", HostPort: "22026"},
					},
				},
			},
		},
	}

	host, port := pickPublishedSSHHostEndpoint(insp)
	if host != "127.0.0.1" || port != 22026 {
		t.Fatalf("unexpected endpoint: got=%s:%d", host, port)
	}
}
