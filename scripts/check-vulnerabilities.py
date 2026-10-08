#!/usr/bin/env python3
"""Fail on callable vulnerabilities, retaining two reviewed daemon-only notices."""
import json
import subprocess
import sys

# These unreviewed reports mark every symbol in the monolithic Docker module.
# The connector embeds the HTTP client, never dockerd, its AuthZ middleware, or
# plugin installation. Keep the notices visible; host engines need independent
# patching. Do not extend this list to silence a new application vulnerability.
DAEMON_ONLY = {"GO-2026-4887", "GO-2026-4883"}

result = subprocess.run(["govulncheck", "-json", "./..."], capture_output=True, text=True)
if result.returncode not in (0, 3):
    sys.stderr.write(result.stderr)
    sys.exit(result.returncode or 1)

decoder = json.JSONDecoder()
remaining = result.stdout.lstrip()
blocked = set()
notices = set()
while remaining:
    record, end = decoder.raw_decode(remaining)
    remaining = remaining[end:].lstrip()
    finding = record.get("finding")
    if not finding:
        continue
    trace = finding.get("trace", [])
    if not trace or not trace[0].get("function"):
        continue  # govulncheck's non-callable package/module inventory
    vuln = finding["osv"]
    if vuln in DAEMON_ONLY and trace[0].get("module") == "github.com/moby/moby" and trace[0].get("version") == "v28.5.2+incompatible":
        notices.add(vuln)
    else:
        blocked.add(vuln)

for vuln in sorted(notices):
    print(f"Reviewed daemon-only notice: {vuln}; keep host Docker patched")
for vuln in sorted(blocked):
    print(f"Callable vulnerability: {vuln}", file=sys.stderr)
sys.exit(bool(blocked))
