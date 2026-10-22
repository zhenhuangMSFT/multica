# Provisioning Windows Multica workers

This first increment provisions a Microsoft Dev Box in an **existing approved
project/pool**, prepares a dedicated Multica CLI profile on that box, and verifies
runtime registration. It does not create Azure infrastructure, install tools,
authenticate, execute remote scripts, start/restart services, register persistent
startup, rebind agents, or migrate sessions. No backend/database is needed on a worker.

For changes to an already enrolled daemon, use the
[controlled upgrade process](../daemon-maintenance/README.md); provisioning is
not an upgrade, drain or rollback mechanism.

The [fleet canary contract](../daemon-maintenance/FLEET-CANARY.md) maps this manifest
to the separate worker logon installer. `New-WorkerLogonConfig.ps1` generates its
JSON offline for the loopback Dev Tunnel topology; it neither installs tasks nor
turns an existing worker into a fresh provisioning target.

Requires PowerShell 7. Creation additionally requires an owner-authenticated Azure
CLI 2.75+ with the `devcenter` extension already installed. Install these prerequisites
through your approved tooling; do not copy authentication caches from another host.
The Azure command contract is documented at
<https://learn.microsoft.com/cli/azure/devcenter/dev/dev-box>.

## Configuration and offline plan

Copy `devbox.example.json` to a private configuration file and replace **all**
example values with the approved endpoint/project/pool, desired devbox name,
worker's actual Windows computer name, Multica endpoints/workspace UUID, and
approved installed tool paths/versions. The Azure devbox name and Windows computer
name need not be equal. Example versions are illustrative, not an upgrade policy.
Keep credentials out of this file; unknown fields are rejected.

From a checkout:

```powershell
pwsh -NoProfile -File .\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json
```

The default `Plan` action is offline and read-only: it does not resolve executables,
contact Azure/Multica, or mutate configuration. Output contains structured argument
arrays rather than shell-evaluated strings. Review it before authorizing changes.

## Create the machine

Run in the owner's provisioning terminal, not an agent task. Obtain cloud cost and
pool approval first:

```powershell
.\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json -Action Create -ApproveCreate -WhatIf
.\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json -Action Create -ApproveCreate
```

Creation lists the caller's boxes in the specified project/endpoint. An exact-name,
same-pool match is reused without creation; a pool mismatch or failed/malformed list
aborts. It never deletes/recreates a machine, falls back after a denial, or retries
an ambiguous create. The cloud operation runs in the foreground. Inspect the returned
provisioning state in Azure; `created_not_enrolled` is **not** a readiness signal.
Pending approval/provisioning returns `provisioning_not_ready`; terminal failures
raise errors rather than reporting creation as completed.
Use a single authorized provisioning owner for a given machine name.

## Prepare the worker

Connect using an approved owner remote desktop/customization channel. On the **new
worker**, install Git, Multica, Codex and Copilot at the pinned paths/versions using
the approved image/package mechanism. Tool installation is deliberately separate:
this increment has no permission to choose enterprise package sources, accept
licenses, upgrade an existing installation, or assume an image's authentication.

Set the actual `computerName` in the configuration. Run in a clean owner PowerShell
terminal with no `MULTICA_*` overrides. Do not remove task-scoped variables inside an
agent session to bypass the host guard. Use an isolated named profile and workspace
root. Keep other operators out of this new profile while preparing it.

```powershell
.\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json -Action Prepare -WhatIf
.\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json -Action Prepare
```

Prepare validates host and installed versions before writes. It uses supported CLI
`config show/set`, preserves tokens and daemon identities, and refuses a profile
with conflicting settings or a running daemon that would need changes. Exact
matches are no-ops. Configuration writes are per-key, not transactional; if a write
fails, inspect the reported error and profile before rerunning. Matching partial
writes and unset remaining values allow resumption. No automatic rollback removes
existing configuration.

Preparation is not enrollment. Verify owner-only API/frontend connectivity from
this worker, including the **non-interactive daemon's** tunnel authentication path.
Never broaden access or tunnel model-proxy ports to make it work.

Complete Multica and provider sign-in locally. The plan gives the exact Multica
`login` command for the configured profile. Unlike `setup`, this does not implicitly
start/restart the daemon. Login discovers the owner's workspaces; review its watch
list before starting a daemon. Do not paste credentials in configuration or comments.

Ensure all four pinned tool directories are in the launching process's PATH (and
the eventual approved startup environment); the daemon discovers providers from
that environment. After explicit daemon-start approval, execute the plan's
`ownerStart` command **on the worker**. Automatic Multica update/reload is disabled.
Persistent startup needs separate approval; this script does not register it.

## Verify and hand off

```powershell
.\scripts\devbox\Invoke-MulticaDevBox.ps1 -ConfigPath .\worker.json -Action Verify
```

Verify is read-only but requires owner context on the target worker. It checks
pinned tool versions, saved settings, local daemon identity/version and both online
runtime registrations in the intended workspace, with heartbeats no older than
120 seconds. API/authentication errors propagate; `auth status` is not used as an
exit-code-only readiness check. Output records the actual daemon/runtime IDs.

`registered_not_smoke_tested` is intentionally not "ready": provider authentication,
effective capacity, repository access, tools/MCP, WebSockets and task completion
still need an explicitly approved disposable end-to-end task. Do not assign real
work on registration alone. A host change does not automatically migrate queued
tasks, provider sessions, local artifacts or unpushed work.

## Local tests

```powershell
pwsh -NoProfile -File .\scripts\devbox\Invoke-MulticaDevBox.test.ps1
```

Tests use in-memory command/filesystem stubs, not Azure accounts, owner profiles,
real provider executables or running daemons. A live provisioning/enrollment
acceptance test still requires the approvals and target details above.
