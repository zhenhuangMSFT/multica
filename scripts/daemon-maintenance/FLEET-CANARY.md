# Fleet integration contract

This is the combined preparation/canary/deployment contract for the existing
bootstrap, daemon-maintenance and Windows worker logon packages. It is not an
installer, live drain implementation or authorization. Use the
[controlled upgrade gates](README.md) for binary changes. After-sign-in recovery
is the accepted service level: no S4U, password startup or automatic sign-in.

## Ownership and inputs

Multica Operator integrates PILOT-17 and the existing PILOT-13 server startup.
Codex Worker owns PILOT-18 remote supervision. PILOT-15 remains parked under
PILOT-10 until the admission contract below is reviewed and work is explicitly
dispatched. One owner-approved external executor owns each maintenance window;
coding agents must not administer the daemon that hosts their task.

| Input | Role and current boundary |
| --- | --- |
| `scripts/devbox/Invoke-MulticaDevBox.ps1` | Offline Plan; separately approved cloud Create; owner-side fresh-worker Prepare; registration Verify. Not a supervisor, updater or migration tool. |
| `scripts/devbox/New-WorkerLogonConfig.ps1` | Offline mapping from validated bootstrap manifest to PILOT-18 installer JSON. No credentials, executable lookup, machine access or installation. |
| `scripts/windows-worker` | Integrated logon installer/supervisor review source, including target-host and update/reload policy guards. Examples use synthetic identities; supply private approved per-host config separately. Not a production updater. |
| `Test-DaemonUpgradeEvidence.ps1` | Checks supplied evidence shape/gates only. It does not observe or implement drain, authorize work, or prove assertions. |
| Existing server `MulticaPilot-*` tasks | Preserve server/daemon/tunnel sign-in configuration; do not run a replacement startup installer on this host. |

The worker package now lives in this checkout; its implementation owner remains
responsible for host adapters. Attachments and uncommitted patches are review
inputs, not reproducible approved release artifacts.
Before deployment assemble a release manifest containing each exact file/hash,
source commit/patch hash, tool versions, target host, private config hash and
rollback reference. Build the daemon fix from a reviewed clean source revision;
do not label a dirty build with its base SHA.

## Reconciled configuration

| Bootstrap field | PILOT-18 field | Invariant |
| --- | --- | --- |
| `worker.computerName` | `DeviceName` | Actual target Windows host; installer must reject a different host. |
| `worker.profile` | `Profile` | Existing `pilot-remote` for the remote canary; never run Prepare to reset its profile. |
| `worker.serverUrl` | `ServerPort` | Connector topology requires root `http://localhost:PORT` or `http://127.0.0.1:PORT`; reject direct HTTPS or path-prefix topology instead of guessing. |
| `worker.workspacesRoot` | `WorkspacesRoot` | Exact existing root; do not replace it with the new-worker example. |
| `worker.maxConcurrentTasks` | `MaxConcurrentTasks` | Preserve approved limit; launch flag is effective and overrides saved settings. |
| `worker.tools.multica.path` | `MulticaExe` | Same pinned absolute executable; record running revision/hash too. |
| Owner-supplied identity | `UserId` | Existing owner's actual SID resolved on the target, not a sandbox identity. |
| Approved tunnel/path inputs | `TunnelId`, `DevTunnelExe`, `PowerShellExe` | Resolve and hash actual executables; verify approved forwarding independently. |

The mapping command writes JSON to stdout only:

```powershell
.\scripts\devbox\New-WorkerLogonConfig.ps1 -ConfigPath .\approved-worker.json `
  -UserId 'DOMAIN\owner' -TunnelId 'approved-tunnel.region' `
  -DevTunnelExe 'C:\ApprovedTools\devtunnel.exe' `
  -PowerShellExe 'C:\Program Files\PowerShell\7\pwsh.exe'
```

Review and save the result as `worker.json` in the durable, access-restricted
package directory on the target. App URL, workspace UUID, provider paths and
versions remain in the bootstrap/release manifest and must be verified separately.
Mapping does not validate identity membership, actual endpoints or installed tools.
Run `Install-WorkerLogon.ps1 -ConfigPath .\worker.json -Mode Plan` there; task names
must be `MulticaWorker-pilot-remote-Tunnel` and
`MulticaWorker-pilot-remote-Daemon` for the current remote profile, at root task path.
Earlier proposed `RemoteTunnel-*` names are superseded by the actual package.

### Worker review v2 and remaining qualification gaps

- PILOT-18 review v2 adds actual-computer guards for Install/Remove/supervisor and
  explicit no-auto-reload beside no-auto-update. It retains portable offline Plan.
  The supplied v2 package replaces v1 for source review, not as deployment approval.
- Daemon readiness currently means TCP connect to IPv4 loopback, not authenticated
  API readiness. Keep startup tolerant of delayed upstream, but expose degraded
  versus authenticated-ready states. No tokens in JSON, argv or logs.
- Installation/per-component locks only serialize cooperating scripts in one
  directory. Wrappers lack a common maintenance gate on initial launch/respawn.
  Scheduled-task retries must not defeat the maintenance executor.
- Install refuses existing names, so it is safe first-install tooling, not fleet
  reconciliation. Add owned-version/action/config comparisons, unchanged no-op,
  collision refusal, approved stopped-task update, and transactional rollback of
  partial registration. Do not blindly replace tasks by description alone.
- Current logs are bounded but health has no structured state/freshness output,
  authentication failure signal or hung-child diagnosis. Verify child cleanup;
  stopping a wrapper is not evidence that all child processes stopped.

These are integration requirements for the package owner, not permission to edit
the running host or independently implement competing wrappers.

The [worker lifecycle module](WORKER-LIFECYCLE.md) now implements strict readiness
and maintenance record consumption plus owned-task update/rollback classification.
The simulation-only transaction coordinator supplies durable envelope/journal
storage and stopped-task update/rollback sequencing. `auth probe` independently
collects bounded authenticated API evidence, but never certifies tunnel forwarding
or provider readiness. None of these enable a production scheduler executor or
shared maintenance authority. Missing trusted production execution and live
qualification remain explicit.

The [read-only snapshot increment](WORKER-SNAPSHOT.md) supplies exact root-task
XML and versioned definition hashes, release/config file hashes and referenced
process identity observations. It does not prove child absence; unknown coverage
keeps transaction updates blocked. No production execution or host qualification
is enabled by collecting a snapshot.

## Shared admission/drain proposal for review

This interface is NOT implemented by a capacity setting, file lock or evidence
boolean. Its complete product boundary is still required before live upgrade/retirement:

The daemon's existing `claimMu`/`pauseClaims`/`claimsInFlight` barrier now includes
[durable local admission ownership](DAEMON-ADMISSION.md): host/profile-bound
holder/generation transitions, explicit crash/expiry recovery and fail-closed
local claim rejection. Clearing an auto-update barrier cannot release a
maintenance holder. This still has no authenticated external maintenance control
command, server-side dispatch enforcement, supervisor respawn fencing or complete
drain grant. Its documented first-write/storage limits remain explicit.

1. Host/profile-authorized maintenance acquisition returns an exclusive lease
   identity/generation and gates all claim paths before acknowledging acquisition.
   The ownership scope includes provider registrations, queue dispatch and
   resource-wait transitions; inspect already dispatched work as well as active
   children. Cooperating deployment and supervisor paths honor this same state.
2. Claims and capacity/maintenance changes synchronize at one admission boundary.
   Gate activation prevents new starts without cancelling active work; race-lost
   dispatch is reconciled without dropping queue items or causing duplicate starts.
   Report saved/effective limits, override source, occupied slots, inflight claims,
   queued/dispatched/resource-wait work, maintenance phase and generation.
3. Drain completion requires no running children or unresolved inflight admissions;
   queued work is retained and resource-wait work explicitly accounted for.
   Capture a checkpoint before stop. A stale owner or daemon restart must not
   silently reopen admissions; recovery/release needs an authorized decision.
4. Release validates lease/generation and continuity evidence. Canary allowance is
   scoped to one approved task/provider, not a general reopening of admissions.
   Lease failure/stale reports stop promotion and surface an actionable error.

PILOT-15 implements synchronized live capacity and saved/effective reporting only
after review of this boundary. Increasing/decreasing capacity must not bypass
maintenance or cancel active runs; zero capacity is not an invented drain API.
No endpoint/CLI command in this proposal is claimed to exist.

## Combined canary: reviewed steps, not commands to execute now

All times below are proposed thresholds, not observed results. Keep raw timestamps
and actual durations even on failure. Baseline and qualification cover both hosts;
do not test only the remote daemon while ignoring the server/tunnel host.

| Stage | External executor action | Required evidence / stop condition |
| --- | --- | --- |
| Baseline | Capture task XML, exact Run entry, owner SID, PID/creation time, running image/hash/revision, flags/environment sources, profiles, daemon/runtime IDs and private rollback inventory. | Both hosts identified; unrelated tasks untouched; no secrets in published evidence. Recheck active work immediately before mutation. |
| Install-only | Review exact candidate/config hashes and target, run installer Plan/WhatIf, then approved registration without starting tasks. | Remote names absent or exact owned-version reconciliation; server tasks unchanged; record each action and partial failure. |
| Remote cutover | Through the approved enforced maintenance path, drain/checkpoint, suppress old launch/respawn, export and remove only the verified redundant Run value, stop exact confirmed old processes, then start the two exact remote tasks. | No competing connector/daemon; no process-name killing. If no enforced path is available, do not cut over. Installation does not imply cutover approval. |
| Readiness | Upstream backend healthy, upstream owner-only tunnel host connected, remote connector forwarding actual API port, authenticated API ready, explicit-profile daemon ready. | Within 120 seconds of dependencies being available: fresh unchanged runtime identities, expected revision/settings, WebSocket wake path. Distinguish frontend 3000 from API 8080; never expose proxy ports 23333/23334. |
| Provider canaries | Narrowly admit one disposable task per required provider with a known input/result and no external side effects. | Result and final issue comment within proposed 5 minutes, correct runtime, isolated workspace, no duplicates. Heartbeats alone fail. Copilot DevBox MCP discovery/read-only call is separate, only after its patch activation. |
| Session/reconnect | Approved lock/RDP-disconnect, delayed upstream/network, and component-crash drills individually, restoring health after each. | No duplicate instances, bounded retry, authenticated readiness and task recovery; record hung-child/expiry failures rather than indefinite retry. Do not sign out as a substitute for locking. |
| Reboot/sign-in | Separate explicit permission: reboot one host at a time, owner signs in, verify same readiness/canary, then the other host. | Downtime until sign-in accepted; measure from sign-in and upstream availability. Existing server tasks preserved. No automatic sign-in/S4U changes. |
| Interrupted work | Approved synthetic interrupted task with checkpoint and harmless effects. | Old/new attempt lineage, durable workdir/context references and effect reconciliation. Eligible retry is a new attempt, not exact continuation. No automatic replay of completed external actions. |
| Upgrade/rollback | Separate source/hash-bound candidate, gated drain, stop/swap/start; verify candidate then explicitly exercise known-good rollback. | Full maintenance gates; identities/auth/workspaces preserved; do not bundle provider upgrades, migrations or server changes. |
| Release | Restore approved admission/supervision; retain private rollback and publish sanitized evidence. | Fail any gate: stop promotion, keep state visible, restore exact baseline under executor control; no expansion on partial success. |

## Checkpoint and evidence format

Store a private JSON record per host/change with `schemaVersion: 1`, UTC
observation times, source/artifact/config hashes, supervisor names/action hashes,
host/profile/daemon/runtime IDs, maintenance ownership reference/generation,
queue/active/inflight snapshot, and stage outcomes with raw-evidence references.
Use explicit `not_run`, `blocked`, `passed` or `failed`; never fill proposed timing
targets as measured values. Reference the existing upgrade-evidence schema for its
own gates; do not add checkpoint fields to that strict input.

The context checkpoint records issue/task/attempt lineage, branch/commit,
dirty/untracked inventory, approved private transfer artifact and hash, worktree
location, provider-session reference and known portability limitations. Issues,
instructions and committed work are durable; uncommitted/unpushed work needs an
explicit transfer. Do not copy provider authentication stores. Retain the old
worker until the destination verifies context and a canary; rebind and retire only
with explicit approval after queued-work reconciliation. Never delete the old
profile/runtime/session stores as an upgrade shortcut.

## Repeatable deployment plan and remaining decisions

1. Package reviewed source into immutable versioned releases with checksums and
   approved scan evidence. Keep scripts/config in a durable owner-controlled
   directory, not an agent worktree. Separate package content from per-host config.
2. Choose an existing authorized management channel. Dev Box project/pool/image
   customization can deliver prerequisites and scripts; a machine-management
   policy can reconcile installed workers. Confirm actual project/endpoint,
   deployment identity, ownership, logging, supported user-context task execution
   and rollback before implementing that adapter. Do not assume SYSTEM is the
   provider-authenticated owner or that a VS Code MCP connection is a deployment
   channel.
3. Implement reconciliation/detection: desired/current release and config hashes,
   exact owned task actions/principal, pinned binaries, profile drift, degraded
   health/freshness and no-op second execution. Reject unknown task collisions,
   drifted profiles and mixed partial updates explicitly. Update only through the
   reviewed maintenance interface; keep a resumable per-step journal.
4. Enroll each host with supported owner/provider/tunnel authentication. Existing
   machines retain their stores; new machines authenticate independently. Check
   license/tenant support before promising unattended token provisioning. Never
   ship tokens in manifests or clone browser/CLI credential caches.
5. Canary first, then bounded explicitly approved batches with per-host outcomes
   and rollback. Keep after-sign-in availability limitations visible: automated
   package deployment does not sign the owner into each rebooted machine.

Read-only discovery for the pilot found Azure CLI available, its current
subscription enabled, and no Dev Center projects returned in that subscription;
the devcenter extension was absent. This does not prove projects are absent from
other subscriptions or management systems. No extension was installed, subscription
changed, MCP activated or cloud resource created. The owner must identify the
approved project/subscription/management policy and execution authority. Intune
enrollment/permission has not been discovered.

GitHub access on the integration host was confirmed for the owner fork; this is
not evidence that the remote worker has publishing credentials. Publication is not
authorized by read-only discovery; use attached artifacts for review meanwhile.

The coordinated approval request, after candidate gaps are resolved, must name:
executor and rollback owner; hosts/profiles and exact package/config hashes;
install versus cutover scope; enforced admission mechanism; window/deadlines;
specific network/crash/reboot/upgrade drills; preserved tasks; stop conditions and
rollback actions. Until then preparation can continue, but shared rollout is gated.
