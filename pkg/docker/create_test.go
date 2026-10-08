package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	backendDocker "github.com/antgpu/connector-protocol"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// gpuDaemon serves just enough of /info for configureGPU's runtime probe.
func gpuDaemon(t *testing.T, runtimes []string) *client.Client {
	t.Helper()
	entries := make([]string, 0, len(runtimes))
	for _, name := range runtimes {
		entries = append(entries, fmt.Sprintf("%q:{\"path\":%q}", name, name))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/info") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Runtimes":{%s}}`, strings.Join(entries, ","))
	}))
	t.Cleanup(server.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("building test client: %v", err)
	}
	return cli
}

func assertGPUsAttached(t *testing.T, hostConfig *container.HostConfig) {
	t.Helper()
	if len(hostConfig.Resources.DeviceRequests) != 1 {
		t.Fatalf("expected one GPU request, got %#v", hostConfig.Resources.DeviceRequests)
	}
	got := hostConfig.Resources.DeviceRequests[0]
	if got.Driver != "nvidia" || got.Count != -1 || !reflect.DeepEqual(got.Capabilities, [][]string{{"gpu"}}) {
		t.Fatalf("unexpected GPU request: %#v", got)
	}
}

func TestConfigureGPU_RequiredTemplateGetsNVIDIA(t *testing.T) {
	hostConfig := &container.HostConfig{}

	configureGPU(context.Background(), gpuDaemon(t, []string{"runc", "nvidia"}), hostConfig, 1)

	if hostConfig.Runtime != "nvidia" {
		t.Fatalf("expected nvidia runtime, got %q", hostConfig.Runtime)
	}
	assertGPUsAttached(t, hostConfig)
}

// Docker Desktop exposes GPUs through device requests and registers no "nvidia"
// runtime. Naming it there fails ContainerCreate outright, so every GPU template
// became unprovisionable on Windows/macOS providers while CPU ones worked.
func TestConfigureGPU_DockerDesktopHasNoNvidiaRuntime(t *testing.T) {
	hostConfig := &container.HostConfig{}

	configureGPU(context.Background(), gpuDaemon(t, []string{"runc"}), hostConfig, 1)

	if hostConfig.Runtime != "" {
		t.Fatalf("named a runtime the daemon does not have: %q", hostConfig.Runtime)
	}
	assertGPUsAttached(t, hostConfig)
}

// An unreachable daemon must not cost the container its GPUs: without the
// runtime it still gets them, with a missing runtime it would not be created.
func TestConfigureGPU_UnreachableDaemonStillAttachesGPUs(t *testing.T) {
	hostConfig := &container.HostConfig{}
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("building test client: %v", err)
	}

	configureGPU(context.Background(), cli, hostConfig, 1)

	if hostConfig.Runtime != "" {
		t.Fatalf("assumed a runtime it could not verify: %q", hostConfig.Runtime)
	}
	assertGPUsAttached(t, hostConfig)
}

func TestConfigureGPU_CPUOnlyTemplateIsUnchanged(t *testing.T) {
	hostConfig := &container.HostConfig{}

	configureGPU(context.Background(), gpuDaemon(t, []string{"runc", "nvidia"}), hostConfig, 0)

	if hostConfig.Runtime != "" || len(hostConfig.Resources.DeviceRequests) != 0 {
		t.Fatalf("CPU template unexpectedly received GPU settings: %#v", hostConfig)
	}
}

func TestParseTemplatePortMapping_DefaultTCP(t *testing.T) {
	got, err := parseTemplatePortMapping("8080")
	if err != nil {
		t.Fatalf("parseTemplatePortMapping returned error: %v", err)
	}
	if got.hostPort != "8080" {
		t.Fatalf("expected hostPort=8080, got %q", got.hostPort)
	}
	if got.containerPort != "8080" {
		t.Fatalf("expected containerPort=8080, got %q", got.containerPort)
	}
	if !reflect.DeepEqual(got.protocols, []string{"tcp"}) {
		t.Fatalf("expected protocols [tcp], got %#v", got.protocols)
	}
}

func TestParseTemplatePortMapping_HostAndProto(t *testing.T) {
	got, err := parseTemplatePortMapping("18080:8080/udp")
	if err != nil {
		t.Fatalf("parseTemplatePortMapping returned error: %v", err)
	}
	if got.hostPort != "18080" {
		t.Fatalf("expected hostPort=18080, got %q", got.hostPort)
	}
	if got.containerPort != "8080" {
		t.Fatalf("expected containerPort=8080, got %q", got.containerPort)
	}
	if !reflect.DeepEqual(got.protocols, []string{"udp"}) {
		t.Fatalf("expected protocols [udp], got %#v", got.protocols)
	}
}

func TestParseTemplatePortMapping_BothProtocols(t *testing.T) {
	got, err := parseTemplatePortMapping("18080:8080/both")
	if err != nil {
		t.Fatalf("parseTemplatePortMapping returned error: %v", err)
	}
	if !reflect.DeepEqual(got.protocols, []string{"tcp", "udp"}) {
		t.Fatalf("expected protocols [tcp udp], got %#v", got.protocols)
	}
}

func TestParseTemplatePortMapping_InvalidProtocol(t *testing.T) {
	_, err := parseTemplatePortMapping("8080/sctp")
	if err == nil {
		t.Fatal("expected error for invalid protocol")
	}
}

func TestParseTemplatePortMapping_InvalidPort(t *testing.T) {
	_, err := parseTemplatePortMapping("abc:8080")
	if err == nil {
		t.Fatal("expected error for invalid host port")
	}
}

func TestEnsureSSHLoopbackPortBinding_AddsRandomLoopbackPort(t *testing.T) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}

	ensureSSHLoopbackPortBinding(exposed, bindings)

	sshPort := nat.Port("22/tcp")
	if _, ok := exposed[sshPort]; !ok {
		t.Fatalf("expected 22/tcp to be exposed")
	}
	got := bindings[sshPort]
	if len(got) != 1 {
		t.Fatalf("expected one binding, got %#v", got)
	}
	if got[0].HostIP != "127.0.0.1" || got[0].HostPort != "" {
		t.Fatalf("unexpected binding: %#v", got[0])
	}
}

func TestEnsureSSHLoopbackPortBinding_PreservesTemplateBinding(t *testing.T) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{
		"22/tcp": []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "2222"}},
	}

	ensureSSHLoopbackPortBinding(exposed, bindings)

	got := bindings["22/tcp"]
	if len(got) != 1 || got[0].HostIP != "0.0.0.0" || got[0].HostPort != "2222" {
		t.Fatalf("template binding changed: %#v", got)
	}
}

func TestLaunchContainer_UsesLocalImageWhenPullFails_Integration(t *testing.T) {
	if os.Getenv("RUN_DOCKER_INTEGRATION") != "1" {
		t.Skip("set RUN_DOCKER_INTEGRATION=1 to run Docker integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create docker client: %v", err)
	}
	defer cli.Close()

	// A tag that exists only on this daemon, under a registry host that cannot
	// resolve. Pull must fail; the launch must succeed anyway.
	localTag := "antgpu-pull-fallback-test.invalid/local-only:1"
	if err := tagLocalTestImage(ctx, cli, localTag); err != nil {
		t.Skipf("could not prepare a local test image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), localTag, image.RemoveOptions{Force: true})
	})

	tmpl := backendDocker.Template{
		TemplateName: "pull-fallback-test",
		ImagePath:    localTag,
	}
	containerID, err := LaunchContainer(ctx, tmpl)
	if err != nil {
		t.Fatalf("launch should have fallen back to the local image, got: %v", err)
	}
	t.Cleanup(func() {
		_ = StopContainer(context.Background(), cli, containerID, nil)
		_ = cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	})
	if containerID == "" {
		t.Fatal("expected a container id")
	}
}

// tagLocalTestImage gives an already-present image a second, unpullable tag.
func tagLocalTestImage(ctx context.Context, cli *client.Client, tag string) error {
	const source = "alpine:3.20"
	if _, err := cli.ImageInspect(ctx, source); err != nil {
		out, pullErr := cli.ImagePull(ctx, source, image.PullOptions{})
		if pullErr != nil {
			return pullErr
		}
		_, _ = io.Copy(io.Discard, out)
		out.Close()
	}
	return cli.ImageTag(ctx, source, tag)
}
