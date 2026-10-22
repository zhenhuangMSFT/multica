# Controlled daemon upgrades

The current [external runtime-management architecture](EXTERNAL-RUNTIME-MANAGEMENT.md)
separates optional fleet/OS policy from minimal generic core lifecycle hooks.
The scripts here are draft extraction candidates, not an installed extension.

Use this process for a source fix or packaged Multica CLI/daemon update on an
existing worker. It does not authorize a rollout. Provider CLI upgrades, backend
changes, database migrations, startup-identity changes and reboots are separate
changes. Do not combine them with a daemon binary replacement.

For the combined bootstrap/worker/server interfaces, canary stages and fleet
deployment plan, see [Fleet integration contract](FLEET-CANARY.md).
For the implemented pure readiness/ownership/update/rollback gates and their
remaining adapter boundaries, see [Worker lifecycle gates](WORKER-LIFECYCLE.md).

## Ownership and approvals

There is one integration owner and one maintenance executor per host. Record the
issue, target host/profile, source commit, scope, maintenance window, approved
executor and rollback owner before changing shared state. Check the owning issues,
bounded comment threads and active runs. An empty run list is not a lock.

All host writers must use the same approved exclusive maintenance entry point:
binary deployment, scheduler changes, startup wrappers and rollback. A named lock
used by only the new script is insufficient. Record the enforced admission-control
mechanism and restoration procedure. Until the existing launchers and operators
participate, this prerequisite is **not implemented**.

Task-scoped agents can prepare/test artifacts and inspect task-safe status. They
cannot administer their host daemon, borrow owner profiles or update their own
running executable. The executor must be outside the target daemon's lifecycle;
stopping the daemon from its own task would orphan the task and its final report.

## Standard sequence

| Gate | Required outcome |
| --- | --- |
| Source | Reviewed exact commit, compatible server/API contract, narrowly scoped change, passing relevant tests. Never deploy a dirty tree represented only by its base SHA. |
| Artifact | Build in an isolated checkout; capture source commit, clean-tree status, toolchain, exact command, OS/architecture, version/embedded revision, executable SHA-256 and test evidence. Preserve the original artifact and previous approved executable. |
| Security | Scan through normal enterprise controls. A quarantine/detection is a stop condition for owner/IT review, not a reason to rename, recopy, exclude or replace the binary to evade detection. |
| Preflight | Explicit rollout approval, target profile/identity confirmed, current PID/revision/runtime IDs, supervisor action/environment, backend/tunnel health, active and queued work, and private rollback inventory captured. |
| Drain | Enforced block on new claims and trigger/admission paths that could race with the change; existing runs finish. Capture daemon active/running/resource-wait counts and server queued/dispatched/running/waiting work. Preserve queued work; do not cancel or reassign it as a drain shortcut. |
| Stop | Under exclusive ownership, use the approved host supervisor/profile-specific stop path. Confirm exact PID exited and cannot be respawned during replacement. Do not kill by executable name. |
| Replace | Verify target hash, preserve the old approved binary, install only the scoped artifact. Preserve daemon ID, owner/profile, workspaces, provider session stores and application credentials. Do not reset registration, run `setup`, delete runtime records or invoke migrations. |
| Start | Start through the same approved supervisor, with the same identity, profile, workspace root and explicit update/reload flags. Keep admissions gated. |
| Verify | Observe new PID and embedded revision, same daemon/runtime identities, backend/tunnel health, WebSocket wake path, provider discovery and task behavior. A CLI version alone is insufficient, especially for same-version patches. |
| Release | Execute one explicitly approved canary task through a narrowly scoped admission exception. Capture its source-bound result and continuity evidence; then restore normal admission and verify the saved supervisor configuration. |
| Close | Record timestamps, hashes, before/after identities, verification results, rollback availability and unresolved limitations. Rollout remains incomplete if any gate is unproven. |

The current CLI has no qualified general-purpose drain command in this workflow.
The [durable local daemon admission boundary](DAEMON-ADMISSION.md) includes a
backend-authenticated exact-owner `runtime maintenance` control surface.
It gates local claims only, not external writers or the full drain sequence.
Do not substitute `max_concurrent_tasks`, a status snapshot, a comment saying
"maintenance", or an assumed mutex. If no enforced admission mechanism is available,
defer the live upgrade and implement/approve one first.

Settings precedence is flags > environment > persisted config > defaults.
Archive sanitized effective settings rather than only `config show`. Keep full
profile/supervisor exports private and access-restricted; publish no tokens,
authentication caches or raw MCP configuration.

## Rollback

Predeclare triggers: startup/health timeout, wrong revision/identity, missing
runtime/provider, failed canary or unexpected task recovery. Specify a bounded
timeout for each gate before the window starts. Failure stops promotion.

If rollback is needed, keep admission blocked, stop only the exact new process
through the approved supervisor, restore the recorded old executable and unchanged
launch configuration, and start/verify the old revision under the same identity.
Record partial outcomes and escalate if rollback fails. Do not automatically retry
deployment, rerun external actions, restore a database or delete workspaces.
Before retrying an interrupted task, inspect its terminal/retry lineage and external
side effects; startup recovery is not exactly-once execution.

## Example: assigned MCP servers absent in Copilot

The candidate change in `server/pkg/agent/copilot.go` forwards the effective
managed MCP configuration with `--additional-mcp-config @<private-temp-file>`.
An enabled assignment alone does not test forwarding, startup or Azure access.

Before activation:

1. Integrate the candidate fix into a reviewed commit, isolated from unrelated
   worker bootstrap changes. Run the fake-child Copilot regressions and compile
   the target platform. Preserve the old approved artifact for rollback.
2. Produce a Windows artifact manifest and security-scan result. The current
   pilot launcher disables auto-update and auto-reload; an update request or
   replacing an on-disk file is not proof that the running daemon changed.
3. Obtain the maintenance executor/admission-control decision. No restart is
   authorized merely by approving source work or requesting orchestration.

After activation, verify the expected revision, unchanged identities and both
registered providers. In a new approved canary run, observe DevBox tool discovery
and one read-only project/pool operation. Do not create/delete/restart a devbox to
test MCP connectivity. If authentication fails, record that separately from MCP
forwarding/handshake. No passing fake-child test proves live Azure authentication.

## Coordination with worker boot/tunnel reliability

For the pilot example, Multica Operator integrates the daemon/MCP change and this
upgrade contract in PILOT-17. The existing PILOT-18 assignee owns the companion
worker/tunnel readiness and rollback evidence for the remote canary, not deployment
of the server host. Do not create duplicate workers or change assignments.

After-sign-in recovery is the accepted pilot service level. PILOT-18's
signed-in-session control passed, but full recovery still needs qualification.
Before-sign-in logon support is not a blocker for this scope. Do not provision
another logon identity or configure automatic sign-in.

Daemon upgrade acceptance and unattended reboot acceptance are separate:

| Scenario | Evidence owner must collect |
| --- | --- |
| Daemon patch | Source/hash-bound startup, same identities/workspaces, canary task and rollback |
| Tunnel reconnect | Approved disruption test, actual forwarded API mapping, authenticated readiness and WebSocket/task recovery |
| Reboot/sign-in | Separate approval, actual reboot followed by owner sign-in, supervised tunnel then daemon startup and task recovery; downtime until sign-in is accepted |
| Credential expiry | Supported reauthentication/escalation path without copying credential stores or silently broadening access |

Do not declare weekly reboot/sign-in resilience based on a successful daemon patch.

## Offline readiness check

`Test-DaemonUpgradeEvidence.ps1` checks a deliberately small, secret-free evidence
record. It neither gathers evidence nor authorizes/mutates anything. A pass means
only that the supplied record satisfies the chosen gate, not that its assertions
were independently verified. The integration owner validates linked raw evidence.
Start with `upgrade-evidence.example.json` (intentionally blocked).

```powershell
pwsh -NoProfile -File .\scripts\daemon-maintenance\Test-DaemonUpgradeEvidence.ps1 -EvidencePath .\upgrade-evidence.json
pwsh -NoProfile -File .\scripts\daemon-maintenance\Test-DaemonUpgradeEvidence.test.ps1
```

Missing fields, wrong types and unknown fields fail. A valid but incomplete record
returns `blocked` and exit code 2 with the missing gates. `preflight` requires
explicit approval, an exclusive executor, enforced admission control, drained
work and rollback artifact; `complete` additionally requires observed activation,
canary, continuity and restored admissions. Both require source/security evidence.
Boolean fields attest to evidence collected at that stage: for example,
`newAdmissionsBlocked` records the drain gate, while `admissionsRestored` records
the later release gate. They are not a simultaneous live-state snapshot.
