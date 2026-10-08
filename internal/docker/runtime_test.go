package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func runtimeInspection() container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Running: true}, HostConfig: &container.HostConfig{NetworkMode: "bridge"}},
		Config:            &container.Config{Labels: map[string]string{"managed-by": "template-manager", "ai.antgpu.runtime": "unsloth", "ai.antgpu.runtime.port": "8000"}, Env: []string{"ANTGPU_RUNTIME_TOKEN=private"}},
		NetworkSettings:   &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"bridge": {IPAddress: "172.18.0.2"}}},
	}
}

func TestRuntimeTargetUsesOnlyInspectedPrivateContainer(t *testing.T) {
	insp := runtimeInspection()
	target, err := runtimeTarget(insp)
	if err != nil || target.URL.String() != "http://172.18.0.2:8000" || target.Token != "private" {
		t.Fatalf("unexpected target: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*container.InspectResponse)
	}{
		{"unmanaged", func(i *container.InspectResponse) { delete(i.Config.Labels, "managed-by") }},
		{"stopped", func(i *container.InspectResponse) { i.State.Running = false }},
		{"host-network", func(i *container.InspectResponse) { i.HostConfig.NetworkMode = "host" }},
		{"other-container-network", func(i *container.InspectResponse) { i.HostConfig.NetworkMode = "container:other" }},
		{"invalid-port", func(i *container.InspectResponse) { i.Config.Labels["ai.antgpu.runtime.port"] = "65536" }},
		{"unknown-runtime", func(i *container.InspectResponse) { i.Config.Labels["ai.antgpu.runtime"] = "shell" }},
		{"public-address", func(i *container.InspectResponse) { i.NetworkSettings.Networks["bridge"].IPAddress = "8.8.8.8" }},
		{"loopback", func(i *container.InspectResponse) { i.NetworkSettings.Networks["bridge"].IPAddress = "127.0.0.1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := runtimeInspection()
			tc.mutate(&i)
			if _, err := runtimeTarget(i); err == nil {
				t.Fatal("unsafe/unavailable runtime accepted")
			}
		})
	}
}
