//go:build windows

package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	protocol "github.com/antgpu/connector-protocol"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

func TestDesktopFirewallTrustBoundary(t *testing.T) {
	for _, host := range []string{"tcp://127.0.0.1:2375", "tcp://remote:2375", "npipe:////remote/pipe/docker_engine"} {
		cli, err := client.NewClientWithOpts(client.WithHost(host), client.WithVersion("1.47"))
		if err != nil {
			continue
		}
		if err := validateWorkloadDaemon(cli); err == nil {
			t.Errorf("accepted unsupported engine %s", host)
		}
		cli.Close()
	}
	good := image.InspectResponse{ID: "sha256:" + strings.Repeat("a", 64), RepoDigests: []string{"docker" + desktopFirewallImage[strings.Index(desktopFirewallImage, "@"):]}}
	if _, err := pinnedFirewallImageID(good); err != nil {
		t.Fatal(err)
	}
	good.RepoDigests = []string{"docker@sha256:" + strings.Repeat("b", 64)}
	if _, err := pinnedFirewallImageID(good); err == nil {
		t.Fatal("unpinned image accepted")
	}
	host := desktopFirewallHostConfig()
	if host.Privileged || len(host.Binds) != 0 || len(host.Mounts) != 0 || host.PidMode != "" || !host.ReadonlyRootfs || len(host.CapAdd) != 1 || host.CapAdd[0] != "NET_ADMIN" || len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || !host.AutoRemove {
		t.Fatal("helper grants more privilege/persistence than necessary")
	}
}

// This test changes only the scoped AntGPU bridge/firewall and creates/removes
// its own containers. LAN/host sentinel URLs must have reachable positive controls.
func TestDockerDesktopIsolationIntegration(t *testing.T) {
	if os.Getenv("RUN_DESKTOP_ISOLATION_INTEGRATION") != "1" {
		t.Skip("requires dedicated Docker Desktop Windows host")
	}
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	urls := []string{os.Getenv("ISOLATION_HOST_URL"), os.Getenv("ISOLATION_LAN_URL")}
	for _, url := range urls {
		if url == "" {
			t.Fatal("reachable host/LAN sentinel URLs required")
		}
	}
	baseline, err := cli.ContainerCreate(ctx, &container.Config{Image: desktopFirewallImage, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}}, &container.HostConfig{CapDrop: []string{"NET_RAW"}}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), baseline.ID, container.RemoveOptions{Force: true})
	})
	if err := cli.ContainerStart(ctx, baseline.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, url := range urls {
		output, err := containerCommand(ctx, cli, baseline.ID, []string{"wget", "-T", "3", "-qO-", url})
		if err != nil || !strings.Contains(output, "ANTGPU_ISOLATION_SENTINEL") {
			t.Fatalf("positive control failed %s: %s %v", url, output, err)
		}
	}
	t.Log("positive controls: host and LAN reachable from ordinary Docker bridge")
	template := protocol.Template{TemplateName: "Windows isolation adversarial probe", ImagePath: desktopFirewallImage, Ports: []string{"31684:8080"}, OnStartScript: "#!/bin/sh\nset -eu\nwhile true; do printf 'HTTP/1.1 200 OK\\r\\nContent-Length: 24\\r\\nConnection: close\\r\\n\\r\\nANTGPU_ISOLATED_INGRESS\\n' | nc -l -p 8080 -w 1 || true; done\n"}
	id, err := LaunchContainer(ctx, template)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveContainer(context.Background(), cli, id, true, true) })
	isolated, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !isolated.State.Running {
		t.Fatal("probe server exited before reachability checks")
	}
	if isolated.HostConfig.NetworkMode != workloadNetwork || isolated.HostConfig.Sysctls["net.ipv6.conf.all.disable_ipv6"] != "1" || isolated.HostConfig.Privileged {
		t.Fatal("workload lacks isolation")
	}
	check := func() {
		t.Helper()
		for _, url := range urls {
			if output, err := containerCommand(ctx, cli, id, []string{"wget", "-T", "2", "-qO-", url}); err == nil {
				t.Fatalf("reached protected service %s: %s", url, output)
			}
		}
		for _, command := range [][]string{{"iptables", "-F"}, {"sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=0"}} {
			if output, err := containerCommand(ctx, cli, id, command); err == nil {
				t.Fatalf("workload changed security boundary: %s", output)
			}
		}
		if output, err := containerCommand(ctx, cli, id, []string{"nslookup", "example.com"}); err != nil {
			t.Fatalf("DNS unavailable: %s %v", output, err)
		}
		if output, err := containerCommand(ctx, cli, id, []string{"wget", "-T", "10", "-qO-", "http://example.com"}); err != nil {
			t.Fatalf("public egress unavailable: %s %v", output, err)
		}
	}
	check()

	auditScript := "exec timeout 180 /bin/sh -c \"while true; do printf 'HTTP/1.1 200 OK\\r\\nContent-Length: 24\\r\\nConnection: close\\r\\n\\r\\nANTGPU_ISOLATED_INGRESS\\n' | nc -l -p 31686 -w 1 || true; done\""
	auditor, err := cli.ContainerCreate(ctx, &container.Config{Image: desktopFirewallImage, Entrypoint: []string{"/bin/sh"}, Cmd: []string{"-ec", auditScript}}, desktopFirewallHostConfig(), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), auditor.ID, container.RemoveOptions{Force: true})
	})
	if err := cli.ContainerStart(ctx, auditor.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	networkInfo, err := cli.NetworkInspect(ctx, workloadNetwork, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	vmURL := "http://" + networkInfo.IPAM.Config[0].Gateway + ":31686"
	if output, err := containerCommand(ctx, cli, baseline.ID, []string{"wget", "-T", "3", "-qO-", vmURL}); err != nil || !strings.Contains(output, "ANTGPU_ISOLATED_INGRESS") {
		t.Fatalf("VM-host positive control failed: %s %v", output, err)
	}
	if output, err := containerCommand(ctx, cli, id, []string{"wget", "-T", "2", "-qO-", vmURL}); err == nil {
		t.Fatalf("workload reached VM host: %s", output)
	}
	counter := func() uint64 {
		t.Helper()
		output, err := containerCommand(ctx, cli, auditor.ID, []string{"iptables", "-nxv", "-L", "ANTGPU-EGRESS"})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, "169.254.0.0/16") {
				fields := strings.Fields(line)
				n, err := strconv.ParseUint(fields[0], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
		}
		t.Fatal("metadata rejection rule absent")
		return 0
	}
	beforeMetadata := counter()
	if _, err := containerCommand(ctx, cli, id, []string{"wget", "-T", "2", "-qO-", "http://169.254.169.254/"}); err == nil {
		t.Fatal("metadata endpoint reachable")
	}
	if counter() <= beforeMetadata {
		t.Fatal("metadata rejection rule did not intercept the request")
	}
	t.Log("VM host protected; metadata rejection confirmed by packet counter")

	httpClient := &http.Client{Timeout: 3 * time.Second}
	response, err := httpClient.Get("http://127.0.0.1:31684")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("loopback ingress broken")
	}
	peerTemplate := template
	peerTemplate.Ports = []string{"31685:8080"}
	peer, err := LaunchContainer(ctx, peerTemplate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveContainer(context.Background(), cli, peer, true, true) })
	peerInfo, err := cli.ContainerInspect(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	peerIP := peerInfo.NetworkSettings.Networks[workloadNetwork].IPAddress
	if net.ParseIP(peerIP) == nil {
		t.Fatal("missing peer IP")
	}
	if output, err := containerCommand(ctx, cli, id, []string{"wget", "-T", "2", "-qO-", "http://" + peerIP + ":8080"}); err == nil {
		t.Fatalf("reached another rental: %s", output)
	}

	response, err = httpClient.Get("http://127.0.0.1:31685")
	if err != nil {
		t.Fatal("peer positive control failed:", err)
	}
	response.Body.Close()
	for _, url := range urls {
		if output, err := containerCommand(ctx, cli, baseline.ID, []string{"wget", "-T", "3", "-qO-", url}); err != nil || !strings.Contains(output, "ANTGPU_ISOLATION_SENTINEL") {
			t.Fatalf("policy broke ordinary provider containers: %s %v", output, err)
		}
	}
	others, err := cli.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(filters.Arg("network", workloadNetwork))})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range others {
		if other.ID != id && other.ID != peer {
			t.Fatal("rule-loss test requires no other active workloads")
		}
	}

	timeout := 5 * time.Second
	if err := StopContainer(ctx, cli, id, &timeout); err != nil {
		t.Fatal(err)
	}

	if err := StopContainer(ctx, cli, peer, &timeout); err != nil {
		t.Fatal(err)
	}
	for _, hook := range [][2]string{{"DOCKER-USER", "ANTGPU-EGRESS"}, {"INPUT", "ANTGPU-HOST"}} {
		if output, err := containerCommand(ctx, cli, auditor.ID, []string{"iptables", "-w", "5", "-D", hook[0], "-i", workloadBridge, "-j", hook[1]}); err != nil {
			t.Fatalf("simulate scoped firewall loss: %s %v", output, err)
		}
	}
	if err := StartContainer(ctx, cli, id); err != nil {
		t.Fatal(err)
	}
	check()
	if err := RestartContainer(ctx, cli, id, &timeout); err != nil {
		t.Fatal(err)
	}
	check()

	helpers, err := cli.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(filters.Arg("label", "antgpu.helper=workload-firewall"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(helpers) != 0 {
		t.Fatal(fmt.Sprintf("%d firewall helpers left running", len(helpers)))
	}
	t.Log("PASS protected host/LAN, peer isolation, DNS/public egress, loopback ingress, privilege denial, guarded stop/start/restart")
}
