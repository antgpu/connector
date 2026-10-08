# AntGPU Connector

The provider agent enrolls a machine with AntGPU, establishes its tunnel, reports
hardware and health, and manages rented Docker workloads.

This private repository is being prepared for release. Public documentation and
licensing are pending. Windows source is retained, but Windows releases and
validation are deferred.

## Build and run

Requires Go 1.26 or newer. Linux workload provisioning requires root, the local
Docker socket `/var/run/docker.sock`, Docker's iptables backend, and a storage
driver with writable-layer quotas. With overlay2, use XFS with project quotas
(`pquota` or `prjquota`). Unsupported isolation or quotas refuse provisioning.
Previously created containers must be drained and recreated to acquire these
limits and the managed network. Images declaring writable volumes are refused
because those volumes bypass layer quotas. Pulled image layers are outside these
quotas; use a separate, capacity-controlled Docker data filesystem.
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
memory, CPU, PID, writable-layer, and log limits. An unspecified disk limit
defaults to 8 GiB. Docker is a shared-kernel boundary; it does not guarantee
isolation against kernel or GPU-driver vulnerabilities.

## Dependencies and updates

`pkg/protocol` contains the dependency-free wire types. Building this repository
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

Before activating signed releases, deploy the backend change that preserves the
manifest `signature` field. Existing connectors do not acquire signature
verification until their binaries are replaced; this is a coordinated migration.

CI retains two explicit daemon-only vulnerability notices for the monolithic
Docker client dependency; keep the provider's Docker engine patched separately.
