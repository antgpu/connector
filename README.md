# AntGPU Connector

The provider agent enrolls a machine with AntGPU, establishes its tunnel, reports
hardware and health, and manages rented Docker workloads.

This private repository is being prepared for release. Project code is licensed
under [MIT](LICENSE); third-party components retain their respective licenses.
Public documentation and the third-party notice inventory are pending. Windows
source is retained, but Windows releases and validation are deferred.

## Build and run

Requires Go 1.26 or newer. Linux workload provisioning requires root, the local
Docker socket `/var/run/docker.sock` and Docker's iptables backend. Any Docker
storage driver may be used. Writable-layer quotas are applied where supported;
other setups, including containerd overlayfs on ext4, use soft storage enforcement.
Unsupported network or resource isolation still refuses provisioning.
Previously created containers must be drained and recreated to acquire these
limits and the managed network. Image-declared volumes and pulled image layers
are outside writable-layer quotas. Storage telemetry reports the enforcement
mode to the backend, which handles reservations, usage monitoring, and stopping
rents at the host safety floor or after sustained excess usage. Soft enforcement
is periodic and cannot prevent a fast write from exhausting disk space.
NVIDIA workloads also require the NVIDIA driver and Container Toolkit.

```sh
go test ./...
go build -o connect ./cmd/connect
./connect --foreground --state-dir /var/lib/antgpu/connect '<ENROLLMENT_TOKEN>'
```

The production API is `https://api.antgpu.ai`. The `dev` build tag uses a local
API and separate enrollment state. Keep `.auth`, tunnel configuration, logs,
and `cache/` private. Run from a trusted, non-writable directory when omitting
`--state-dir`.

Rented containers publish on loopback. A managed bridge blocks new connections
to the provider host, private networks, and cloud metadata addresses; public
egress and replies to inbound connections remain available. Containers have
memory, CPU, PID, and log limits, plus writable-layer quotas where supported.
An unspecified hard disk quota defaults to 8 GiB. Docker is a shared-kernel boundary; it does not guarantee
isolation against kernel or GPU-driver vulnerabilities.

## Dependencies and updates

The public `github.com/antgpu/connector-protocol` module contains the dependency-free
wire types shared with the backend. Building this repository
does not require access to the private backend. The pinned public tunnel/Nebula
fork remains in use to preserve its port-forwarding extensions. Nebula's optional
diagnostic SSH server is disabled.

Developer builds have no update trust key and refuse fleet updates. For release
binaries, `bash scripts/build-release VERSION` embeds the reviewed public key in
`.github/connector-update.pub`. `cmd/release-manifest` signs binary checksums with
the corresponding offline Ed25519 key supplied through `CONNECTOR_SIGNING_KEY`.
Never commit the signing key. Clients verify the manifest signature and binary
checksum before restarting.

Pushes and pull requests run CI. The publishing workflow runs only by manual
dispatch from `master`, after checks pass, with the exact confirmation
`publish-active-update`. It requires the `connector-production` environment,
signing and publishing secrets, explicitly configured artifact storage, and pinned
SSH host keys. Configure required environment reviewers and branch/workflow
protection before public release; declaring an environment alone adds no review
requirement. No production credentials have been transferred to this repository.

The proposed [maintainer policy](.github/REPOSITORY_POLICY.md) permits direct
`master` pushes by `@mintyleaf`, with CI afterward. Other contributions require
a reviewed PR and passing checks. Publishing stays a separate manual action.

Before activating signed releases, deploy the backend change that preserves the
manifest `signature` field. Existing connectors do not acquire signature
verification until their binaries are replaced; this is a coordinated migration.

On Linux, a new release launched from the legacy updater cache authenticates a
snapshot of its running executable against the pinned signed active manifest,
atomically replaces the stable service executable, and execs that installed
copy. It preserves enrollment and the service configuration. Existing systemd
services are discovered by the current PID and invocation, including direct
executables and the literal `exec` wrappers used by older installers; a new
installation-path environment setting is not required. Ambiguous custom wrappers
fail closed and require the state-preserving installer. Non-systemd legacy
processes need an explicit `ANTGPU_CONNECT_INSTALL_PATH` for durable migration.
The old updater still relies on its original HTTPS/checksum trust until it starts
the new binary; an old executable cannot gain signature verification retroactively.

CI retains two explicit daemon-only vulnerability notices for the monolithic
Docker client dependency; keep the provider's Docker engine patched separately.
