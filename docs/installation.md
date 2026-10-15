# Installation and support

The connector enrolls a provider machine and controls its local Docker engine.
Use a dedicated trusted host. Install the NVIDIA and Docker prerequisites first;
the connector installer checks them and does not erase Docker data or uninstall
drivers. Enrollment requires successful native compute, compatibility, stress,
and network admission. Docker Model Runner is an optional additional runtime.

## Supported boundaries

| Platform | Requirements | Validation |
| --- | --- | --- |
| Linux amd64 | Native Linux, systemd, Python 3, OpenSSL 3+, local Docker with iptables, NVIDIA driver and Container Toolkit | Physical Ubuntu 22.04.5 / RTX 4060 Ti, fresh NVIDIA 615.71.09 open driver and Docker 29.9.0; original NVIDIA 610.57.04 proprietary driver and Docker 29.7.2 also validated |
| Windows amd64 | Native Windows supported by Docker Desktop, Administrator installation, Docker Desktop WSL2 Linux engine running, NVIDIA GPU-PV support | Physical Windows 10 / RTX 4060 Ti, PowerShell 5.1, WSL 2.6.3, Docker Desktop 4.82.0 / engine 29.6.1, NVIDIA 610.88 |
| Linux arm64 | Same Linux requirements | Builds and CI checks; physical GPU validation pending |

Windows 11 physical coverage is pending. Follow the operating system and hardware
requirements of [Docker Desktop](https://docs.docker.com/desktop/setup/install/windows-install/)
and your NVIDIA driver. The Windows bench result does not extend vendor support
to an operating system outside its supported servicing lifecycle.

Running the Linux connector inside WSL is unsupported and the Linux installer
stops before changing the host. Install the native Windows connector instead.
Docker Desktop's internal WSL2 Linux engine is supported; it does not make WSL
itself a supported Linux provider host. Native Windows containers and remote
Docker daemons are unsupported. Use protected all-users Docker installation paths;
an ordinary user-writable Docker executable cannot be trusted by a SYSTEM service.

## Linux

Obtain a one-time enrollment token from AntGPU and save it in a private local
file. Download the HTTPS installer, inspect it if desired, then install:

```sh
curl --proto '=https' --fail --show-error --silent https://get.antgpu.ai -o install.sh
sudo bash install.sh --token-file /private/path/enrollment.token
```

Remove the operator-owned token input file after successful enrollment. The
installed service retains its machine identity; it removes consumed tokens from
its managed service environment. The default service is `antgpu-connect`, with
private state under `/var/lib/antgpu/connect` and a stable installed executable.
Use `--inspect` to review the selected installation without changing it.

The installer preserves valid identity by default. Existing interactive installs
offer preserve/reset/cancel. `--preserve-state` explicitly repairs an existing
installation without a new token; `--reset-state` requires a new token and makes
a private backup. `--yes` suppresses prompts and does not imply reset. Other
installations are listed and left untouched. Drain active rentals through AntGPU
before reinstalling. Unsafe or ambiguous partial state requires explicit recovery.

## Windows

Open Docker Desktop in the provider's desktop session and select its Linux engine.
Ensure it starts when that user signs in. Keep its engine available for the
connector service, including after reboots. Verify `docker info` and a Docker GPU
container before enrollment. Install from an elevated PowerShell session:

```powershell
Invoke-WebRequest 'https://get.antgpu.ai/win' -UseBasicParsing -OutFile install.ps1
. .\install.ps1
Install-AntgpuConnect -TokenFile 'C:\private\enrollment.token'
```

Remove the operator-owned input file after successful enrollment. Defaults are
service `antgpu-connect`, executable under `C:\Program Files\Antgpu\Connect`,
and private state under `C:\ProgramData\Antgpu\Connect`. `-Inspect` reviews the
selected installation; `-PreserveState` repairs it without reenrollment.
`-ResetState` requires a new token and makes a protected recovery backup.
`-Force` suppresses prerequisite/choice prompts and does not erase identity.
Drain active rentals before reinstalling. Credentials and cached updates require
SYSTEM/Administrator access; the desktop tray does not grant access to private logs.

With a connector release supporting `--no-popup`, pass `-NoPopup` to the installer
to prevent the log viewer from opening automatically during installation or repair:

```powershell
Install-AntgpuConnect -TokenFile 'C:\private\enrollment.token' -Force -NoPopup
# Repair an enrolled machine without opening its log window:
Install-AntgpuConnect -NoPopup
```

File logging and the tray's View Logs action remain available. `-NoPopup` affects
this installation, not future manual log viewing. Use `-Force` separately to
suppress installation prompts. The installer refuses an older signed binary
without this option before stopping the existing service or changing enrollment.

The checksum-pinned bootstrap verifier authenticates the signed manifest before
the installer executes a connector binary. Both the installer and its verifier
must be deployed together as described in [verifier packaging](../cmd/verify-update/README.md).

## Model Runner

On Linux, install the Docker Model plugin and its CUDA runner. A first engine
download can exceed the connector's bootstrap deadline; complete the official
installation before using model rentals:

```sh
docker model install-runner --gpu cuda --port 12434
docker model status
```

On Windows, enable GPU-backed inference in Docker Desktop's Model Runner settings.
Current tested Desktop versions also support:

```powershell
docker desktop enable model-runner --gpu=enable --tcp=12434 --cors=none
docker model status
```

Confirm the CUDA backend rather than accepting a CPU-only healthy response.
Model rentals expose a rent-authenticated proxy through AntGPU; keep the local
runner endpoint private. The small model used for physical inference validation
was `ai/smollm2:360M-Q4_K_M`; other models need their own capacity checks and licenses.

## Updates and recovery

The production channel distributes signed manifests and checksum-verified binaries.
Updates defer during active rentals, preserve identity, and survive a service or OS
restart. Failed checks back off. A repository push runs CI; publishing an active
fleet update is a separate manual action. Operator credentials and signing keys
must stay outside source control.

Use the state-preserving installer to repair a failed service path. Keep recovery
backups private and do not overwrite current credentials with an older snapshot
for an ordinary binary rollback. Do not start managed containers manually after
a Docker/VM restart; the connector must reapply its network policy first.

Storage setup is flexible: hard writable-layer quotas apply where supported,
otherwise storage enforcement is periodic. Fast writes, image layers, and volumes
can exhaust disk space between checks. No filesystem migration is required.

The isolation boundary is Docker's shared kernel and GPU driver, not a separate
virtual machine per tenant. See [Windows isolation](windows-workload-isolation.md)
and the main README for the enforced network and resource limits.


## Benchmark reuse and trusted hosts

A backend supporting benchmark policy stores successful results across connector
restarts, reboots, repairs and ordinary self-updates. Standard validity is 7 days
for GPU performance/compatibility, 30 days for stress, and 6 hours for throughput.
An administrator can mark a machine trusted in the admin Machines benchmark panel:
trusted validity is 30 days, 90 days, and 24 hours respectively. Initial successful
admission remains mandatory. There is no provider-controlled trust/skip flag.

A cached reboot probes the GPU/driver/Docker identity and checks Docker GPU
availability and the tunnel. GPU/runtime or benchmark-image changes invalidate
GPU results; network address/source changes invalidate throughput. The connector
checks for due or admin-requested validation every five minutes and obtains an
exclusive backend reservation before expensive tests. It defers during existing
rentals and blocks new provisioning during benchmarking. Results retain their
original successful timestamps when reused; skipped or failed checks do not renew
validity. ECC remains unsupported.

Network-only renewal invokes the pinned image's iperf3 directly without GPU access.
Compatibility/stress-only runs discard any unsolicited performance metrics emitted
by older pinned images; the CUDA runner source also skips workload/tensor scoring
when GPU performance is not requested. Rebuilding/publishing that image is a
separate release operation. The current published image remains compatible.

Deploy the new backend API before releasing the connector. Against an older API,
the connector retains full legacy startup admission. Admin trust requires both
backend and connector support; it is not a workaround for failed readiness checks.
