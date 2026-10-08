//go:build linux

package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

// Run only on a disposable Linux Docker host: this installs the scoped firewall.
func TestWorkloadIsolationIntegration(t *testing.T) {
	if os.Getenv("RUN_ISOLATION_INTEGRATION") != "1" {
		t.Skip("requires disposable Linux Docker host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	host := &container.HostConfig{}
	if err := configureWorkload(ctx, cli, host); err != nil {
		t.Fatal(err)
	}
	if host.Memory <= 0 || host.MemorySwap != host.Memory || host.NanoCPUs <= 0 || host.PidsLimit == nil || *host.PidsLimit != 1024 {
		t.Fatalf("unbounded resources: %#v", host.Resources)
	}
	port := nat.Port("8080/tcp")
	host.PortBindings = nat.PortMap{port: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}}}
	created, err := cli.ContainerCreate(ctx, &container.Config{Image: "busybox:1.37.0", ExposedPorts: nat.PortSet{port: struct{}{}}, Cmd: []string{"sh", "-c", "mkdir -p /www; echo isolated > /www/index.html; exec httpd -f -p 8080 -h /www"}}, host, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	})
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := cli.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.NetworkSettings.Ports[port]) != 1 {
		t.Fatalf("missing published port: state=%#v ports=%#v", info.State, info.NetworkSettings.Ports)
	}
	if got := info.NetworkSettings.Ports[port][0].HostIP; got != "127.0.0.1" {
		t.Fatalf("published on %s", got)
	}
	endpoint := "http://127.0.0.1:" + info.NetworkSettings.Ports[port][0].HostPort
	clientHTTP := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	for attempt := 0; attempt < 20; attempt++ {
		response, err = clientHTTP.Get(endpoint)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("loopback ingress broken: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "isolated") {
		t.Fatalf("ingress response: %s", body)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "host-secret") }))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	netInfo, err := cli.NetworkInspect(ctx, workloadNetwork, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, hostPort, _ := net.SplitHostPort(listener.Addr().String())
	blockedURL := "http://" + netInfo.IPAM.Config[0].Gateway + ":" + hostPort
	if _, err := containerCommand(ctx, cli, created.ID, []string{"wget", "-T", "2", "-qO-", blockedURL}); err == nil {
		t.Fatal("workload reached provider host")
	}
	// A reachable service on a different private Docker network models a LAN
	// service. It must be reachable from the provider and blocked from the rent.
	lan, err := cli.ContainerCreate(ctx, &container.Config{Image: "busybox:1.37.0", Cmd: []string{"sh", "-c", "mkdir -p /www; echo lan-secret > /www/index.html; exec httpd -f -p 8080 -h /www"}}, &container.HostConfig{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), lan.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	})
	if err := cli.ContainerStart(ctx, lan.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	lanInfo, err := cli.ContainerInspect(ctx, lan.ID)
	if err != nil {
		t.Fatal(err)
	}
	lanURL := "http://" + lanInfo.NetworkSettings.Networks["bridge"].IPAddress + ":8080"
	for attempt := 0; attempt < 20; attempt++ {
		response, err = clientHTTP.Get(lanURL)
		if err == nil {
			response.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("LAN test service not reachable from provider: %v", err)
	}
	if _, err := containerCommand(ctx, cli, created.ID, []string{"wget", "-T", "2", "-qO-", lanURL}); err == nil {
		t.Fatal("workload reached private LAN service")
	}
	if err := ensureBoundedImage(ctx, cli, "busybox:1.37.0"); err != nil {
		t.Fatal(err)
	}
	volumeImage, err := cli.ContainerCommit(ctx, lan.ID, container.CommitOptions{Changes: []string{"VOLUME /unbounded"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = cli.ImageRemove(context.Background(), volumeImage.ID, image.RemoveOptions{Force: true}) })
	if err := ensureBoundedImage(ctx, cli, volumeImage.ID); err == nil {
		t.Fatal("accepted image volumes that bypass writable-layer quotas")
	}
	if output, err := containerCommand(ctx, cli, created.ID, []string{"nslookup", "example.com"}); err != nil {
		t.Fatalf("DNS blocked: %s %v", output, err)
	}
	if output, err := containerCommand(ctx, cli, created.ID, []string{"wget", "-T", "10", "-qO-", "http://example.com"}); err != nil {
		t.Fatalf("public egress blocked: %s %v", output, err)
	}
	// This disposable host deliberately lacks disk quotas. Provisioning must
	// refuse it before pulling an image or starting an unlimited container.
	warning, quotaErr := CheckStorageOptsSupport(ctx, cli)
	if warning != "" || quotaErr != nil {
		if err := configureWorkloadStorage(ctx, cli, &container.HostConfig{StorageOpt: map[string]string{}}, 1024); err == nil {
			t.Fatal("accepted host without disk quotas")
		}
	}
	t.Log("loopback ingress and public egress pass; provider host and private LAN access blocked; hard limits applied; missing quotas rejected")
}

func containerCommand(ctx context.Context, cli *client.Client, id string, command []string) (string, error) {
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", err
	}
	stream, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var output strings.Builder
	_, _ = stdcopy.StdCopy(&output, &output, stream.Reader)
	info, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return output.String(), err
	}
	if info.ExitCode != 0 {
		return output.String(), fmt.Errorf("exit code %d", info.ExitCode)
	}
	return output.String(), nil
}
