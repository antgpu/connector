# AntGPU Connector

The provider agent enrolls a machine with AntGPU, establishes its tunnel, reports
hardware and health, and manages rented Docker workloads.

Project code is licensed under [MIT](LICENSE). The closed-source AntGPU backend is
separate; this agent builds using public dependencies. [Bundled third-party
notices](internal/licensing/NOTICES.md) retain upstream licenses, including embedded
Swagger UI assets. Release binaries contain these notices; `connect --licenses`
prints them without starting the agent or changing machine state.

Native Linux and native Windows with Docker Desktop's local WSL2 Linux engine are
supported boundaries. See [installation and support](docs/installation.md) and
[Windows workload isolation](docs/windows-workload-isolation.md). Linux amd64 and
Windows amd64 were tested on physical NVIDIA hardware, including clean dependency
installs, signed automatic updates, OS reboots, GPU rentals, SSH, and model inference.
Linux arm64 is built and checked in CI; physical arm64 validation is pending.

## Build and run

Requires Go 1.26 or newer. Linux workload provisioning requires root, the local
Docker socket `/var/run/docker.sock` and Docker's iptables backend. Any Docker
storage driver may be used. Writable-layer quotas are applied where supported;
other setups, including containerd overlayfs on ext4, use soft storage enforcement.
Unsupported network or resource isolation refuses provisioning on Linux and
Windows Docker Desktop; native Windows containers and other hosts are unsupported.
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

Rented containers publish on loopback. A managed bridge on Linux and Windows
Docker Desktop blocks new connections to the provider host, private networks,
and cloud metadata addresses; public
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
binaries, `bash scripts/build-release VERSION` builds Linux amd64/arm64 and Windows amd64 and embeds the reviewed public key in
`.github/connector-update.pub`. `cmd/release-manifest` signs binary checksums with
the corresponding Ed25519 key supplied through `CONNECTOR_SIGNING_KEY` from
the protected GitHub release environment.
Never commit the signing key. Clients verify the manifest signature and binary
checksum before restarting. Artifact downloads allow five minutes; manifest
checks retain a 30-second deadline. Concurrent downloads are serialized, rentals
are checked again before replacement, and failed background polls back off to
15 minutes.

Pushes and pull requests run CI. The publishing workflow runs only by manual
dispatch from `master`, after checks pass. It defaults to `mode=stage` with
`confirm=stage-connector-release`: artifacts are registered without changing the
stable version. Backend admins can offer that staged release to explicit machine
IDs, inspect their status, then promote separately. Pausing stops new offers and
retains existing canary pins. Direct fleet activation remains available only with
`mode=fleet` and `confirm=publish-active-update`. Deploy the backend rollout
support before using these modes. It requires the `connector-production` environment,
environment-scoped signing and publishing secrets, server-controlled immutable
artifact storage, and pinned SSH host keys. The upload key grants only a restricted
artifact receiver, without shell access. The release environment requires
`@mintyleaf` approval and allows only `master`; admin bypass is disabled. Existing
organization administrators retain the ability to change repository protections.

The applied [maintainer policy](.github/REPOSITORY_POLICY.md) permits direct
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

On Windows, service executables require protected local directories and ACLs that
exclude write access by ordinary desktop tokens. State/cache credentials allow
SYSTEM and Administrators only. The service uses transient SCM start arguments
for first enrollment, and removes legacy persisted tokens after enrollment.
Updates register an absolute executable path in the persistent private cache;
a native helper waits for SCM to stop the old service before starting the new
binary. The installer preserves enrollment by default; `-ResetState` makes a
private backup, while `-Force` only suppresses prerequisite/choice prompts.
Windows bootstrap scripts go through Docker's copy API, without granting Docker
Desktop's file-sharing process access to private host state. Tray status remains
unprivileged; private logs and service controls require an administrator.

The Windows installer uses an immutable, checksum-pinned
[bootstrap verifier](cmd/verify-update/README.md) to authenticate the same signed
manifest as the updater. Upload that exact verifier artifact before deploying the
new PowerShell installer. No verifier checksum or public-key override is accepted.
This extra bootstrap artifact is independent of routine connector version updates.


Windows workload isolation uses a short-lived, digest-pinned helper inside Docker
Desktop's Linux VM, with only NET_ADMIN and no host mounts or Docker socket. The
connector rechecks policy before start, restart, and resume; rentals cannot
automatically restart outside that gate. See [Windows workload isolation](docs/windows-workload-isolation.md)
for prerequisites, lifecycle, and validation.
