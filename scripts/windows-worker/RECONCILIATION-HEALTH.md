# Owned-task reconciliation and health interface proposal

This describes the host-adapter integration boundary, not an implemented production updater, health service or maintenance API. After-sign-in service is the accepted scope. Preserve existing upstream startup tasks. Shared executable policy and the simulation coordinator are in `../daemon-maintenance/`; see `WORKER-LIFECYCLE.md` there for implemented portions and the bounded `auth probe` API collector.

## Implemented in this revision

`WorkerPolicy.ps1` compares DeviceName to `[Environment]::MachineName` using ordinal case-insensitive equality. Install and Remove, including WhatIf, check before identity lookup, locks and scheduler operations. The supervisor checks before creating logs, taking locks or inspecting/launching children. Existing managed-task guards remain. Offline Plan deliberately supports other target hosts; it is not an installation preflight.

The daemon argument builder adds explicit `--no-auto-reload` beside `--no-auto-update`, preserving profile, device name, concurrency and workspace arguments as separate values. This matches the supplied bootstrap policy; it does not activate a binary update. Ship the helper with both callers. No config schema change is required by the mapper.

## Desired and observed ownership records

For the `pilot-remote` example the only task identities are root-path `\MulticaWorker-pilot-remote-Tunnel` and `\MulticaWorker-pilot-remote-Daemon`. Use separate explicit TaskPath and TaskName fields in every scheduler operation. Never discover ownership by wildcard, task description or display name alone.

Proposed private deployment receipt: schemaVersion, installationId, host, resolved owner SID, profile, existing daemon/runtime IDs, immutable release path, source revision/patch hash, package file hashes, exact private config hash, task identities, canonical task-definition hashes, generation, prior receipt digest, and transaction phase. Keep receipts and version directories writable only by the authorized owner/admin deployment path. A receipt is provenance, not a maintenance lease or authorization.

Canonical task definitions must include executable, argv, working directory, principal SID/logon type/run level, user-specific logon trigger and all scheduler settings including enabled state, multiple-instance policy, retry and execution timeout. Normalize only documented scheduler serialization differences (namespace/attribute ordering and explicitly equivalent defaults), not arbitrary values; retain original XML for rollback. Compare source/config bytes by SHA-256, not timestamps. Resolve executable links and record actual target hashes. A package description version alone is insufficient.

Under the approved exclusive entry point, read both exact tasks, receipt and immutable files, then classify:

| Observed state | Decision |
| --- | --- |
| Neither task nor receipt exists | Fresh install only with explicit install approval; no starts implied. |
| Both tasks and files exactly match a verified receipt and desired release/config | No-op; no registration, stop, restart or receipt rewrite. Report independent health degradation without treating it as configuration drift. |
| Both tasks match verified previous receipt; desired release/config differs | Plan owned update; execute only after approval, maintenance acquisition and drained/stopped evidence. |
| Task exists without trusted receipt, or action/principal/config differs from receipt | Collision/drift: refuse adoption, replacement and removal. Human-reviewed baseline reconciliation required. |
| Only one task, or interrupted transaction journal exists | Recovery-required; never assume a new installation. Validate journal and current exact hashes before a deliberate resume/rollback. |
| Missing/tampered files, wrong host/SID, unsupported receipt schema | Fail closed; retain evidence. |

The existing v1 install has no receipt. Do not silently claim its tasks as owned. A future one-time adoption procedure must compare independently approved full XML/action/config/file hashes and identity under maintenance approval, then record a baseline. This is a distinct approval from ordinary no-op detection.

## Update transaction and rollback

1. Plan desired/observed hashes without mutations. Validate exact host/SID, profiles and file permissions. Store candidate in a new immutable directory; never overwrite scripts referenced by running tasks.
2. An external authorized executor acquires the shared maintenance lease/generation described by PILOT-17. Require new admissions blocked, inflight admissions resolved, active children drained, resource-wait work accounted for and queues preserved. Suppress scheduler/logon/manual respawn through that same enforced path. Stopped task state alone does not prove stopped children.
3. Capture private old XML, task enabled states, receipt/config/file hashes and exact child PID/creation time/image/profile. Capture the legacy Run entry only if explicitly included in the window. Durably journal `prepared` with expected-before hashes before any task replacement.
4. Re-read tasks/receipt under the exclusive path and reject mismatches. Confirm old tasks/children stopped through the approved lifecycle mechanism. Register candidate definitions disabled, one at a time, journaling intended and completed steps. Re-read and hash each registration. A crash between write and journal update is resolved by comparing actual old/new hashes, never by blindly repeating writes.
5. With both definitions verified, journal `registered`; enable/start only as the cutover approval permits, connector before daemon, while ordinary admissions stay gated. Verify authenticated dependency health, same identities and a narrowly admitted provider canary. Commit receipt atomically and journal `verified` before controlled admission release. Record release separately; restart recovery must not infer release from `verified`.
6. On partial registration/readiness/canary failure, keep the gate. Stop only candidate task trees verified by PID, creation time, executable and profile; re-read before acting. Restore both exact old task definitions/config references/enabled states and receipt, remove only transaction-created definitions whose hashes still match, then verify the old release. Any unexpected drift or rollback failure stops automation for executor review.

Do not restore authentication stores, delete workspaces, change runtime bindings, retry external actions or roll back a database. Package/task rollback is distinct from daemon-binary rollback; an old daemon may retain the known Copilot MCP defect. Logon startup or scheduler retries must not race these steps. The current directory lock provides none of this exclusivity or admission control.

## Authenticated readiness adapter (proposed interface)

Add a reviewed, pinned local adapter invoked under the configured signed-in owner, using that profile through supported authentication. It must not borrow agent tokens or copy credential stores. Configure only its approved executable/hash and non-secret expected identity references in a future versioned config. This revision does not add executable hooks.

One invocation has a 10-second hard deadline and returns one bounded JSON object (maximum 16 KiB). Required fields: schemaVersion=1, observationId, observedAtUtc, host, profile, expectedWorkspaceId, state, reasonCode, apiAuthenticated, forwardedEndpointVerified, and durationMs. Enum states: `ready`, `waiting_dependency`, `reauth_required`, `failed`; exit codes respectively 0, 2, 3, 1. Mismatched exit/state, timeout, invalid schema, future/stale timestamp or wrong identity is failure. Ignore no unknown success assertion. No tokens, raw HTTP bodies, headers, MCP configuration or command lines in output.

Readiness must issue a supported authenticated read to the configured loopback API, validate successful structured response and expected workspace/access identity, and independently bind the connection to the approved tunnel/listener and actual forwarded API port. A TCP connection, anonymous health endpoint, tunnel metadata lookup, HTML login page or HTTP 200 alone never qualifies. Refuse redirects/alternate origins to avoid sending credentials elsewhere. Treat transient connection/5xx failures as dependency delay; distinguish 401/expired authentication from 403/access mismatch. Endpoint and supported owner-auth resolution still need integration-owner review; no new API route is claimed to exist.

Connector launches independently to establish forwarding. Daemon launch waits for authenticated API readiness and a valid maintenance decision; pending/failed authentication must not start it. Re-check after startup and periodically: loss of connectivity marks degraded service and backs off, without killing healthy children or retrying tasks. Any admission change belongs to the core shared boundary, not the readiness adapter.

## Health record and maintenance boundary

Proposed atomic, access-restricted per-component JSON health record: schemaVersion, observationId, observedAtUtc, expiresAtUtc, host/profile, component, installationId/release/config hashes, task identity, wrapper/child PID plus creation time, state, reasonCode, lastExitCode, retryCount, nextRetryAtUtc, readiness observation reference, maintenance generation/reference, and daemon/runtime IDs when observed. Suggested update interval 15 seconds, stale after 45 seconds; these are proposed limits, not measurements. Consumers treat missing, malformed, future or expired evidence as unknown/degraded. Readers must validate identities and hashes.

Component states: `waiting_signin` (external observer only), `waiting_dependency`, `starting`, `ready`, `degraded`, `reauth_required`, `maintenance`, `failed`, `stopped`. Do not equate adapter API-ready with provider readiness. Aggregate ready additionally requires exact running revision/settings, same runtime IDs with fresh heartbeats, expected process count and references to successful approved provider canaries/WebSocket wake verification. Preserve per-provider results and freshness. A wrapper that stops writing while its child lives is unknown, not healthy; automatic hung-child termination remains unimplemented. Raw child logs may contain sensitive data and require private retention/redaction review.

Maintenance consumer interface: query an authenticated decision bound to host/profile, holder, generation and freshness; consult before initial child launch and each respawn. `held/draining` denies ordinary start, a specific canary allowance is narrowly scoped, and stale/unavailable/unknown decisions fail closed for new starts. Existing children are not cancelled by a query failure. Shared state must survive daemon restart, and the external executor must own stop/release decisions. This is a required interface contract, not a new CLI command or local lock implementation.

## Required follow-up tests and gates

Future isolated tests must cover exact no-op without scheduler writes; unknown collisions; drift and changed SID; stopped owned update; running child refusal; second registration failure; crash before/after each journal write; stale generation; rollback failure; malformed/expired health; wrong workspace; redirects and login HTML; 401/403/5xx; timeout; and loss/recovery without duplicate child/task starts. Cross-directory installers and logon/scheduler respawns must be tested against the real shared admission boundary once implemented.

This bounded revision tests the host guard and launch policy only, plus existing installer checks and mapper compatibility. Reconciliation, authenticated readiness/health production code, enforced maintenance integration, hung-child cleanup, owner-side installation/cutover and approved live recovery tests remain gaps. No live registration, startup mutation, restart, reboot, provider canary or binary upgrade was performed. Publication is via this review artifact; integration belongs to Multica Operator, with no overlapping core-daemon or PILOT-15 changes.
