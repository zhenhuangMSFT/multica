# Durable local daemon admission

`server/internal/daemon/maintenance_admission.go` extends the existing `claimMu`
boundary with durable host/profile-bound holder/generation state. Backend-mediated
owner control is implemented in `server/internal/daemoncontrol` and
`runtime maintenance`. This is source infrastructure, **not a complete drain
capability or deployment permission**. The local daemon listener has no new
maintenance mutation endpoint.

## Implemented boundary

Startup loads admission and command receipts before health serving, registration
or polling. A never-maintained profile retains existing open-startup behavior.
Bad, mismatched or incomplete persisted state fails startup, not an empty-state
fallback. The exclusive profile process lock is the single-writer assumption.

Acquisition holds `claimMu` through generation validation and persistence. The
same mutex rejects new HTTP/WS batch-claim intents while held. Already-issued
claims finish their normal handoff, incrementing active ownership before leaving
the inflight count. Active runs, preparation and resource-wait transitions continue;
the gate does not cancel, reassign or delete queued/active work.

Transient update/reload/refresh barriers and server-requested updates cannot
clear durable maintenance. Acquisition refuses an existing transient barrier or
update. Local status reports inflight claims, active tasks, running providers and
resource-wait separately. Local quiescence requires blocked, healthy admission
and every count zero. **Complete drain is always false.**

## Approved owner authority

The owner approved these source-implementation choices:

1. Backend-authenticated human-only requests with exact runtime/daemon-owner
   equality. Workspace admin status alone grants no maintenance authority.
2. Daemon-wide scope, including every runtime/workspace registration. Every owner
   must resolve to the requesting human, with membership in every affected
   workspace. Mixed, missing or excessive registration sets are refused.
3. Backend-dependent acquisition, recovery and release. Backend loss never clears
   an already-held local gate; there is no offline/local authentication bypass.

These are approved choices, not outstanding decisions. This does not approve
deployment, live maintenance, restarts or interruption. After-sign-in behavior,
parked capacity work and production-disabled worker transactions are unchanged.

Loopback origin, local health, task tokens, OS identity alone, or the CLI's
environment/workdir guard do not authenticate an owner. The control does not
reuse the owner-or-admin runtime-update permission or send saved owner credentials
to a local listener. It is distinct from the existing database-maintenance API.

## Authenticated delivery and results

Human routes are `POST /api/runtimes/{runtimeId}/maintenance` and
`GET /api/runtimes/{runtimeId}/maintenance/{requestId}` under backend `Auth`,
`RequireHumanActor` and a handler backstop. Managed-task/cloud credentials are
refused, including tasks acting for the correct owner. The CLI's task guard runs
before configuration access, but is not the server authentication boundary.

The typed command has explicit UUID, operation, daemon ID, actual OS host,
profile, holder, expected generation, UTC issue time and holder expiry. All keys
are required, including empty strings/zero generation and zero timestamps where
inapplicable. Unknown, duplicate,
wrong-case, null, missing, oversized and trailing JSON are refused. Delivery
expires two minutes after issue time. Backend operations/client requests have
ten-second budgets and 16-KiB message limits. Clients do not follow redirects or
automatically retry commands.

Shared Redis stores one active command per daemon and retains request/results
for 24 hours. Missing/failing Redis returns 503; there is no production
process-local fallback. Identical IDs/payloads return the existing request;
changed payloads conflict. Atomic delivery is **at most once**: a lost claim
response is not redelivered and becomes `indeterminate` after the deadline.
Redis persistence, availability and ACL qualification remain deployment
prerequisites, not guarantees established by the isolated source tests.

The daemon polls after authenticated heartbeats, at most once per 15 seconds per
process. Claim/result routes use `DaemonAuth`: a daemon token must match the
exact daemon/workspace, or the supported human PAT/JWT fallback must equal the
owner of every registration. Task/cloud identities cannot claim commands.
Ownership and a deterministic registration-set digest are rechecked at delivery
and result acceptance. The inventory query admits at most 512 registrations;
513 or more is a refusal, never a silently truncated authorization set.
These are authorization snapshots, not atomic fencing of future registrations.

The daemon independently matches its configured ID and loaded actual host/profile,
not the configurable display name. Results bind the entire command, owner, scope,
observation time and sanitized counts/persistence/recovery flags. Mismatched
results/acknowledgments are refused. No credential, raw persistence error,
command line or filesystem path is returned. A completed result is historical;
submit a new `status` command for a fresh observation.

## Durable replay and ambiguous outcomes

Before execution, the daemon exclusively creates/syncs an immutable
`maintenance-commands/<uuid>.intent` in its profile. A separate bound `.result`
is created/synced after the local transition. Identical completed replay returns
the saved result without execution, including after restart. Changed payloads
conflict. An intent without a result is never automatically executed again.

On startup, an unresolved mutation intent at the current/newer generation closes
admission and requires explicit recovery, including when release committed but
its result did not. A successful newer recovery generation supersedes that old
uncertainty; replay of the old ID still cannot execute again. Failed receipt
storage latches the running process closed. Torn, inaccessible, orphaned or
binding-mismatched receipts fail startup. The 1,024-command receipt limit requires
reviewed offline archival on exhaustion, never automatic deletion/reset.

After ambiguous transport failure, read the original request ID. Do not generate
a replacement acquisition/release as an automatic retry. An indeterminate result
requires a new authorized status observation and, where needed, explicit
generation recovery. No daemon resumes undelivered commands or authenticates
offline from receipts.

## Holder, recovery and persistence ordering

Every successful acquire/recover advances the durable generation by one.
Holder strings are bounded/nonempty and expiry is future-dated, at most 24 hours
from issue time. Expiry is ownership metadata, **never a reopening timer**.
Wrong holder/generation, expired or reloaded holders cannot release.
Explicit recovery installs a new holder/generation without reopening admission.
Before expiry it requires restart/reload observation, not live-holder overwrite.
Persistence faults require healthy reload before recovery; corruption requires
reviewed repair rather than blind retry.

`maintenance-admission.jsonl` is a bounded 1-MiB append-only journal. Records bind
version, actual host, profile, holder, generation, held/open state and expiry.
Load rejects duplicate, unknown, null, missing/wrong-case keys, skipped
generations and wrong binding. Exhaustion requires offline archival.

Under `claimMu`, acquisition latches closed, creates/syncs a `.held` fence, then
appends/syncs the held record before acknowledgment. Release appends/syncs the
open record, removes the fence, then clears in-memory admission. Generation is
retained. Every persistence failure reports failure and latches closed.
Surviving fence plus valid released history requires recovery; fence with
missing/corrupt journal fails startup. No torn tail is silently truncated.

**First-write limit remains an unresolved decision:** if storage refuses every
initial durable intent, including command receipt and admission fence, no durable
acquisition is acknowledged. This process remains faulted/closed, but a replacement
cannot reconstruct an event never recorded. A separately durable authority or
explicitly approved closed-by-default startup policy is still required for that
case. Neither is silently adopted. Do not restart to bypass a reported fault.

Syncs/fault injection qualify process-crash/reload logic, not power-loss/storage
controller behavior. Directory metadata flush, filesystem qualification, protected
ACL/provenance, malicious same-account/privileged writers and path replacement
remain production prerequisites. Symlink refusal is not an authenticated lease.

## Paths outside complete drain

| Path | Boundary |
| --- | --- |
| HTTP/WS batch claims | New intents rejected; already-issued requests finish and are counted. |
| Backend queue/dispatch/reservation/retry | Not fenced. Queued work is preserved; no cancellation/reassignment manufactures drain. |
| Already-owned tasks and resource wait | Continue into provider execution; all ownership counts must reach zero. |
| Supervisor, scheduler, respawn, other profiles | Not fenced. Restart reloads held admission but ordinary registration still runs; orphan lineage needs qualification. |
| Installer, replacement and rollback writers | Not authorized/fenced. Production worker transactions remain disabled. |
| Health, forwarding, WebSocket/provider, canary | Separate evidence/approvals. Local counts cannot prove them. |
| Maintenance control | Exact-owner local admission/recovery implemented; no complete-drain lease, renewal or protected external executor. |

PILOT-15 remains parked. Deployment channel, per-host authentication, approved
external executor/window and live interruption/sign-in qualification are separate
owner decisions. GitHub PR-link repair remains a platform dependency.

## Isolated verification

```powershell
Set-Location .\server
go test ./pkg/protocol ./internal/daemoncontrol ./internal/daemon ./internal/cli ./cmd/multica -run 'TestMaintenance|TestRuntimeMaintenance|TestTryEnterClaim|TestTrySetClaimBarrier|TestHandleUpdate|TestTryAutoUpdate|TestTrySelfReload' -count=1 -timeout 120s
```

Set `REDIS_MAINTENANCE_TEST_URL` to an isolated Redis fixture for real atomic
delivery/result and concurrent replay checks. Tests use unique daemon/request
keys and never flush a database; only those Redis tests skip without the variable.
Other fixtures exercise real authentication middleware, CLI/handler wiring,
strict input/results, crash/reload, persistence failures, queue preservation,
acquire-versus-claim races and stale recovery/release. No live profile, queue,
provider executable, maintenance operation or host service is used.
