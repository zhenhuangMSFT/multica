# PILOT-17 canary execution and rollback package

This is an owner/external-executor handoff, **not an executable deployment**.
Its first executable preparation is offline verification. All live stages below
are **blocked/not run** until their individual gates are satisfied. Do not run
the logon installer, acquire live maintenance, start a task, swap a binary or
reboot from an agent-managed task. Do not remove task identity variables.

Use alongside [the integration contract](FLEET-CANARY.md) and
[controlled upgrade gates](README.md). This package does not implement missing
production adapters, complete drain, protected storage or atomic revocation.

## Pinned inputs and acceptance ledger

| Surface | Source/evidence | What is and is not accepted |
| --- | --- | --- |
| Combined optional source | PR #3, `c9b64bbc5b529480ae4210845dc095abd8c0873a` | Three transport corrections accepted in review `01a1683f-8e75-7104-85c4-681cd8dc0451`. Review inspected code and supplied logs; reviewer did not execute Go. PR remains draft; not release approval. |
| Executor, authority and TLS | `executor`; transport correction evidence on PILOT-17 | 282 passing records / 39 top-level tests; 710 repeated records / 110 top-level executions. Real TLS over in-memory pipes, fake processes only. Not a deployed executor or external manager. |
| Independent Copilot fix | PR #4, `b0aa908d810b8a281d681719ac07b9cfbb6ac83a` | Separate candidate; do not merge it into this package or claim MCP forwarding is active. |
| XKZ6N | PILOT-13 final handoff `01a16213-b534-77ea-a39a-fa1b9c4804ab` | Enabled/running sign-in tasks were observed on October 22. No reboot/recovery qualification; this is historical, not current health. |
| G5JHL | PILOT-18 `01a1621d-f784-72ba-bf67-ac740fe14df0`, current issue blocked | Owner installer prepared; no supplied installation/cutover proof. Existing `pilot-remote` identity must survive. Remote GitHub authentication blocker is historical; integration-host publication works and does not prove remote credentials. |
| Fleet scale | Existing Dev Box discovery in FLEET-CANARY | No approved deployment project/pool/policy established. Previous empty project query was limited to one subscription; do not infer tenant-wide absence. |
| Live capacity | PILOT-15 | Parked. Do not dispatch, change capacity, retire or rebind agents under this package. |

The attachment manifest pins the actual packaged files and their SHA-256 hashes.
It is an integrity inventory, not a code-signing certificate or security scan.
No daemon executable, production certificate, secret, private per-host config or
deployable release is included. Examples are synthetic and must not be installed.

## Already settled: do not request these again

- After-sign-in availability is accepted (`01a1618f-fced-7573-93cc-31e6b55ab554`).
  Preserve XKZ6N tasks. Downtime until each host's owner sign-in is expected;
  no S4U, password startup, automatic sign-in or new user identity is requested.
- External-service/minimal-core architecture and reversible source preparation
  are approved. The continuation/review cycle does not authorize installation,
  privilege grants, account/key provisioning, merge or deployment.
- Source correction review above is complete. Do not re-dispatch those findings.
- Existing response-triggered oversight remains enabled through October 29;
  preserve actor filters. Acceptance is outstanding, so do not disable it now.

## Phase 0: reversible offline preparation (safe now)

1. Verify each archive file against `package-manifest.json` using
   `Get-FileHash -Algorithm SHA256`. Stop on missing/mismatched files. Keep the
   original archive and raw verification logs; never treat a new hash as approval.
2. In PowerShell 7, from `runtime-manager`, run `.\Test-Package.ps1`. This copies
   source into disposable directories and runs offline fixture suites only.
   For executor tests use the exact named-file commands in `executor\README.md`
   from a checkout containing the pinned public lifecycle Go contract. The
   external package has no independent Go module; do not pretend it is a binary.
3. Portable plan only, from `runtime-manager\scripts\windows-worker`:

   ```powershell
   .\Install-WorkerLogon.ps1 -ConfigPath .\pilot-remote.example.json -Mode Plan
   ```

   This is synthetic output, not host readiness. Do not run Install/Remove,
   including WhatIf, from this task.
4. Populate a separate private per-host inventory on the approved owner channel:
   actual owner SID/session, tunnel ID/mapping, executable/version/hash, profile,
   workspace root, daemon/runtime IDs, effective flags, exact task XML and exact
   legacy Run value/type/data. Publish only sanitized evidence references/hashes.
   Do not read or clone authentication caches. This inventory is still missing.

## Phase 1: decisions required before any live execution

Record one source/hash-bound authorization on PILOT-17 naming:

1. **Execution authority and window:** owner-operated or a qualified external
   executor outside both target daemons, rollback owner, exact target host/profile,
   start/end time and allowed actions. Separate install-only from cutover,
   lock/RDP-disconnect, network/crash, reboot/sign-in and binary rollback drills.
   No such live window is established by this source handoff.
2. **Enforced exclusive path:** how every current initial launcher, retry,
   supervisor, operator and admission path is fenced throughout change/rollback.
   A comment, empty run list, directory lock, zero capacity or local maintenance
   receipt is insufficient. Existing wrappers do not implement this shared path.
   Until qualified, stop at Phase 0 even if a maintenance window is approved.
3. **Exact candidate and restore inputs:** approved immutable release, scan
   outcome, actual private config hash, previous executable hash/revision,
   exact baseline launch registrations and context checkpoint. A source ZIP
   cannot fill these values. No new binary build/install is implied.
4. **Fleet delivery target (later expansion, not initial offline work):** approved
   management product/project/subscription/pool or policy plus authorized
   execution identity and read-only discovery scope. Retain after-sign-in
   requirements; do not request cloud creation/spending yet.

If selecting the proposed privileged Windows executor for production, separately
approve its actual service identity/session-token rights, protected key and
enrollment/revocation authority, IPC ACL and journal storage as specified in
`executor\TRUST-CONTRACT.md`. Those operational choices are not needed for
offline tests and do not reopen the accepted architecture. Production providers,
process adapters and storage qualification must then be implemented/reviewed;
approval alone cannot make the current library execute live.

## Phase 2: host-specific execution card (future; blocked)

Do not turn this table into automatic retry logic. Before each mutation refresh
PILOT-13/18/17 active work and verify exclusive ownership; snapshots are not locks.

| Order / target | Action after approval | Required result and abort condition |
| --- | --- | --- |
| 1 / both hosts | Capture private baseline and context checkpoint before touching anything. | Owner SID/profile/daemon/runtime IDs, exact PID + creation time + command/hash, task XML, Run value, worktree/commit/dirty inventory, active/queued/inflight work and approved context-transfer hash known. Unknown field means abort. |
| 2 / G5JHL only | Review actual config in durable owner-controlled directory; Plan, owner-side Install WhatIf, then separately approved install-only. | Exact root names `MulticaWorker-pilot-remote-Tunnel` and `MulticaWorker-pilot-remote-Daemon`; AtLogOn/Interactive, explicit profile, preserved workspace root and both no-auto flags. Existing names/collisions mean abort, not overwrite. Nothing starts during registration. |
| 3 / G5JHL only | Qualified executor closes all participating admission/respawn paths; finish existing work and checkpoint before exact cutover. | Complete drain is currently false, so **this row cannot execute with the supplied adapters**. Never cancel queue items or rely on heartbeat/TCP alone. Export exact redundant Run value before approved removal. |
| 4 / G5JHL only | Stop exact verified legacy children through approved profile/supervisor control; confirm no respawn, then start exact new tasks in tunnel-before-daemon order. | Compare each PID with the reported daemon PID; never terminate the workspace daemon or processes by name. Unknown child remains unknown: stop promotion, do not guess exit. Preserve all unrelated registrations. |
| 5 / both hosts | Recheck server/backend, owner-only tunnel, forwarded API port 8080, authenticated readiness, unchanged runtime IDs, WebSocket wake and one approved harmless provider task per required provider. | Proposed budget: 120 seconds after dependencies available; task 5 minutes. Record actual UTC times even if slower. Frontend 3000 is not API readiness. Never expose proxy ports 23333/23334. Failure/wrong identity/duplicate/expired evidence means rollback decision. |
| 6 / one host at a time | Individually approved delayed-upstream, disconnect, exact-child crash, lock/RDP-disconnect and reboot/sign-in drills. Restore healthy baseline between drills. | Owner signs in after reboot; measure from sign-in and upstream availability. XKZ6N tasks remain unchanged. No sign-out substituted for lock. No automatic repeated disruption. |
| 7 / explicit separate candidate | Approved interrupted-work and daemon upgrade/rollback qualification. | Preserve request/effect lineage; eligible retry is a new attempt, not exact continuation. Artifact hash, scan and rollback executable required. PR #4 provider activation is separate; no MCP success inferred from mocks. |
| 8 / integration owner | Compare raw evidence, identities, effective settings, context and actual rollback result. | Only then consider release/expansion. No retirement, PILOT-15 dispatch, merge or broad rollout from partial success. |

## Rollback card (future; same approval/exclusive path required)

- Trigger on wrong revision/identity, unexpected registrations, duplicates,
  unhealthy dependencies, deadline breach, failed canary or uncertain children.
  Keep admissions gated. Do not run a fresh request ID to repair ambiguous work.
- **Before installation:** rollback is simply stop preparation; no host change
  has occurred. Retain existing tasks, profiles, binary and credentials.
- **Registration-only failure:** identify exactly the tasks created by this
  attempt and compare exported actions/principal/config, not description alone.
  If running or drifted, refuse removal and escalate. The candidate Remove mode
  is not a sufficiently strong fleet ownership verifier.
- **After cutover:** use approved exact-profile control to stop new wrappers and
  account for children; restore recorded old executable/hash and launch config,
  the exact exported Run value and any exact changed task XML. Start only the
  original approved launch path; verify same IDs/auth/workspaces and one canary.
  Stopping a wrapper is not evidence its children exited.
- **Partial/failed restore:** keep status visible/blocked, preserve journal and
  workspaces, stop promotion and ask the designated rollback owner for a recovery
  decision. Never restore a database, erase receipts, steal a lock, delete a
  profile/runtime or copy credentials. No broad process termination.
- Release admission only after approved generation/continuity evidence and
  restored supervision. A source simulation is not proof rollback happened.

## Evidence to return

Keep `not_run`, `blocked`, `passed` and `failed` explicit for every stage, with
UTC start/end, observed duration, host/profile/daemon/runtime identities,
candidate/config/restore hashes and sanitized raw-evidence attachment references.
Never fill timing targets as measurements. Keep private XML/config/Run exports
and credential-bearing logs out of issue attachments.

`upgrade-evidence.example.json` remains deliberately blocked. Its strict validator
checks only the asserted shape/gates, not the truth of observations. Do not set
`exclusivePathEnforced`, `existingRunsDrained`, `rolloutApproved` or `canaryPassed`
to true for this preparation. Integrated fleet acceptance remains outstanding;
PILOT-17 stays in progress.
