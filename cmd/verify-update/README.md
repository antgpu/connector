# Windows installer bootstrap verifier

This small standalone helper verifies the connector-protocol Ed25519 manifest and
returns only the selected Windows artifact metadata. It never installs services,
enrolls a machine, or runs another binary. Its public-key pin must match
`.github/connector-update.pub`; no command-line key override exists.

PowerShell 5.1 has no native Ed25519 verification API. The installer downloads
this helper into a private directory and checks a checksum embedded in the
reviewed installer before executing it. The installer script itself remains an
HTTPS bootstrap trust decision, as with the Linux installer.

The immutable artifact is `installer-verifier-v2/connect-windows-amd64.exe`.
Build from connector commit `faa12a2` (protocol v0.1.0) with Go 1.27.1.
The protocol module version is part of the Go build information and changes the
checksum even when its source is identical:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath \
  -ldflags '-s -w -buildid=' \
  -o dist/installer-verifier-v2/connect-windows-amd64.exe ./cmd/verify-update
shasum -a 256 dist/installer-verifier-v2/connect-windows-amd64.exe
```

The reviewed SHA-256 is
`7bf96f53327bec8283876982328ff6e577b2d27d68eac830a2501f0aa1687d9c`.
Git metadata is excluded so the checksum does not depend on the checkout commit
or dirty status. Version 1 was used only in the private rehearsal; its artifact
remains immutable.

Compare against `backend/scripts/connector/install_connect_win.ps1` before
uploading to the configured connector artifact root. A different toolchain or
source change requires a new immutable version and a reviewed installer pin;
never overwrite this artifact in place. The active connector manifest does not
need to select the verifier as a connector update.

Deploying the artifact/installer and publishing Windows releases remain separate,
explicit release steps. The normal publishing workflow builds Linux amd64/arm64 and Windows amd64.
