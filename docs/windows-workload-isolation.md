# Windows workload isolation

Supported boundary: native Windows connector service controlling Docker Desktop's
local Linux engine through its named pipe. Native Windows containers, remote
Docker engines, and platforms without the required firewall/resource capabilities
fail closed. This does not enable Windows publishing; release approval is separate.

Rentals use the owned `antgpu-workloads` bridge (`antgpu0`) with inter-container
communication disabled and IPv6 disabled. Published ports bind to IPv4 loopback.
The VM firewall blocks new connections from that bridge to VM-host services,
private/loopback/link-local/CGNAT/multicast destinations, metadata endpoints
(including Azure's special IPv4 endpoint), and the Windows provider's interface
IPv4 addresses. Established replies remain available for gateway SSH and forwarded
application ports. Docker DNS and public IPv4 egress remain available.

A pinned `docker:29.5.2-dind` image supplies the VM's iptables tools:
`sha256:6b9cd914eb9c6b342c040a49a27a5eb3804453bae6ecc90f7ff96133595a95e8`.
Docker verifies the pull digest; the connector checks RepoDigests and starts its
immutable image ID. There is no mutable-tag or alternate-image fallback. First
provisioning needs Docker Hub access and image-cache space; this is a separate
third-party image with its own component licenses. No Docker daemon is started
inside the helper.

The helper shares only the VM network namespace, drops all capabilities except
NET_ADMIN, uses a read-only root filesystem and bounded resources, and receives
no Docker socket, host filesystem mounts, host PID namespace, rental scripts, or
credentials. It runs fixed iptables commands through the Docker exec API without
a shell. It is removed after configuration; if the connector crashes, its bounded
sleep expires and Docker removes it. There is no persistent privileged sidecar.
Rentals receive none of the helper's privileges and cannot change the firewall or
reenable IPv6. Existing provider firewall decisions remain in force.

Checks run before provisioning and immediately before container start, including
after long image pulls. Explicit start, restart, and resume also restore/check the
policy. Containers with legacy networking or an automatic restart policy are
refused: drain/recreate them through the platform. After a VM restart, workloads
stay stopped until the connector checks the restored engine and reinstalls policy.
An administrator who directly edits Docker networking/firewall rules or starts
containers outside the connector bypasses this lifecycle and must drain rentals.

If Docker Desktop security settings prevent host-network NET_ADMIN operations,
or its engine lacks the DOCKER-USER iptables chain, provisioning fails with the
underlying error. The connector does not disable Desktop security settings or
fall back to an unfiltered bridge. Provider Windows Firewall configuration is
unchanged. A public egress policy permits externally published Internet services;
it does not replace authentication for those services or prevent vulnerabilities
in Docker, the shared Linux kernel, or GPU drivers.

Physical integration validation uses `RUN_DESKTOP_ISOLATION_INTEGRATION=1` with
reachable benign Windows/LAN sentinel URLs. It verifies positive controls before
blocked requests, VM-host isolation, metadata rejection counters, tenant isolation,
public DNS/HTTP, loopback ingress, denied firewall/IPv6 changes, guarded lifecycle,
recovery after scoped firewall loss, and helper cleanup. Run only on a dedicated
bench with no other active workloads. Standard Windows CI runs the unit checks;
Linux CI also exercises its existing disposable Docker isolation integration.
