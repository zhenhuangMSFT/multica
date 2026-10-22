# Durable local daemon admission

`server/internal/daemon/maintenance_admission.go` extends the existing daemon
`claimMu` admission boundary with local host/profile-bound holder/generation
state. It is source infrastructure, **not a complete maintenance/drain API or
deployment permission**. No CLI/HTTP control surface is exposed in this increment;
only daemon-internal transition methods and isolated tests use it.

## Implemented boundary

Daemon startup loads the profile-local state before starting the health service,
registration, claim poller or task dispatch. Missing state for a never-maintained
profile retains the existing open behavior. Bad/mismatched/incomplete state
returns a startup error, never a successful empty-state fallback.

`acquireMaintenance` holds `claimMu` while checking the expected generation and
persisting acquisition. It blocks new local batch-claim intents on the same
mutex as the HTTP/WS claim poller. It does not wait for idle or cancel anything:
claims already in flight finish normal handoff, incrementing `activeTasks`
before `exitClaim`. Existing preparation, provider execution and local-directory
resource-wait tasks continue under their original contexts.

Transient auto-update/self-reload/runtime-refresh barriers and server-requested
updates reject acquisition while durable maintenance is held. Their
`releaseClaimBarrier` clears only their own transient `pauseClaims` flag, never
maintenance ownership. Conversely, maintenance acquisition refuses an existing
update/barrier rather than claiming another writer's ownership.

The internal observation reports inflight claims, `activeTasks`, live provider
count and resource-wait count separately. Resource wait remains part of active
ownership, including its transition into provider execution. `LocalQuiescent`
requires held admission, a healthy persistence state and every count zero;
**`CompleteDrain` is always false**. These observations are not the worker
maintenance-evidence schema, external authorization or a fenced lease.

## Ownership and recovery

Each successful acquire/recover advances the durable generation by exactly one.
Acquisition is compare-and-set against the last generation, including across
release/restart. Holders are bounded nonempty strings. Expiry must be future-dated
and at most 24 hours from the caller's clock; it is ownership metadata, **not
an automatic admission reopening timer**.

An expired holder cannot release. Explicit recovery installs a new holder and
generation while keeping admission closed. Before expiry, takeover requires
restart/reload observation rather than a live unexpired-holder overwrite. After
restart, the old holder cannot directly release: it must explicitly recover into
a new generation first. Wrong holders/generations, even when an old process
retains its credentials or reads a newer state, cannot release another holder.
This is an internal concurrency contract; authenticated control authorization
and a real external monotonic fencing service are still absent.

## Owner control transport decision (not implemented)

The existing local daemon listener does **not** authenticate an owner principal:

| Existing surface | Actual boundary | Why it cannot authorize maintenance |
| --- | --- | --- |
| `internal/daemon/health.go`: `serveHealth`, `shutdownHandler` | Loopback HTTP; health/status and shutdown do not establish a calling owner identity. | Loopback origin, PID, host/profile fields and a matching health response are not authentication. Do not copy this mutation pattern. |
| `health.go`: `activeRepoCheckoutTask` | Bearer mapped to an active managed task. | This deliberately authenticates the identities maintenance must reject, not a human operator. |
| `cmd/multica/cmd_agent.go`: `requireHumanLocalCommand` | CLI environment/workdir task-context guard. | A caller can bypass the CLI and send HTTP directly; the guard is not server-side authentication. |
| `internal/middleware/auth.go`: `Auth`, `internal/handler/actor_guards.go`: `RequireHumanActor` | Backend-validated user identity and machine-credential exclusion. | These are supported backend boundaries, but are not mounted in the local daemon and supply no existing authenticated owner-to-local-daemon maintenance transport. |
| `internal/handler/runtime_update.go`: `InitiateUpdate`, `runtime.go`: `canEditRuntime` | Backend update command for a runtime owner **or workspace admin**. | Reusing update permission would silently grant a non-owner admin host/profile-wide maintenance authority; runtime/workspace scope is not automatically local profile scope. |

The backend's human-only guard must follow real `Auth`; trusting incoming
`X-User-ID`/`X-Actor-Source` headers in a local handler is not equivalent.
Likewise, task-token prefixes or absent task environment variables cannot prove
an owner's identity. Do not forward a saved owner API token to an unverified
local listener, mint a shared file secret, or expose a placeholder maintenance
endpoint to fill this gap.

**Decision required before wiring status/acquire/recover/release:** choose the
authority and transport for the receiving daemon, not just the CLI syntax.

1. **Recommended: backend-mediated owner commands.** Reuse backend `Auth` and
   `RequireHumanActor`, with exact runtime-owner equality (not the existing
   owner-or-admin update permission), an explicit binding covering the entire
   daemon/profile, and typed delivery/results over an authenticated daemon
   channel. Approve the daemon-wide/multi-workspace ownership rule and behavior
   when the backend is unavailable during recovery. Command IDs, replay/dedup,
   target daemon/host/profile and holder/generation must survive ambiguous
   delivery; an existing version-update request is not that protocol.
2. **Alternative: a separately approved local owner control channel.** Specify
   Windows named-pipe ACL/peer authentication and owner SID/profile binding,
   client/server identity verification and credential lifetime. Decide whether
   managed tasks running under the same Windows SID are explicitly in the
   trusted OS boundary or require a separately isolated broker/credential.
   Same-SID ACL access alone does not distinguish owner terminal from an agent
   child. No such peer-authenticated transport is implemented in this checkout.

These are design choices, not implemented alternatives or a request to broaden
live access controls. Do not choose or deploy one by inference. Until approved,
there is no `daemon maintenance` CLI command or local maintenance HTTP route;
internal transition methods remain unexposed. This blocker is independent of
the first-fence-write durability decision below.

After that decision, verification must exercise the actual CLI-to-authorized
receiver, not a mock that assumes `isOwner: true`: reject missing/invalid,
managed-task and wrong-owner credentials before reading counts or state;
validate host/profile, operation-specific holder/generation and strict bounded
JSON; return sanitized counts and persistence/recovery reason codes without
raw errors/paths/tokens; reject stale recovery/release and persistence faults;
preserve `completeDrain: false`. Include transport replay/target checks and
unauthenticated direct-call fixtures. No live acquisition is needed for those
tests.

## Persistence and failure ordering

The daemon's existing exclusive **profile process lock** is the only allowed
writer owner. External processes must not edit/append this store or pretend that
its files are an enforced host maintenance interface.

`maintenance-admission.jsonl` is a bounded 1-MiB append-only transition journal.
Records bind schema version, actual OS host name, profile, holder, generation,
held/open state and expiry. Full records and transitions are validated on load;
duplicate, unknown, null, missing or wrong-case JSON keys, skipped generations
and wrong host/profile fail closed. Exhaustion requires reviewed offline archival,
not automatic truncation or generation reset.

Ordering under `claimMu`:

1. Latch in-memory admission closed.
2. Create/sync a separate `.held` fence before appending/syncing the held record.
   Only then acknowledge acquisition.
3. For release, append/sync the open record first, remove the fence second, and
   only then reopen in-memory admission. Release retains the last generation.

Every persistence failure returns an error and latches the current process
closed. Ambiguous journal-write acknowledgements, torn records and failed fence
removal do not reopen after reload: the held record or surviving fence keeps the
state closed, or startup returns an error. A surviving fence with an otherwise
released valid history needs explicit generation recovery. A fence with a
missing/corrupt journal requires reviewed offline repair; the code does not
invent a prior generation or silently truncate a torn tail.

**First-write limit:** if storage refuses the fence itself before any durable
intent can be written, no durable acquisition is acknowledged. The current
process remains faulted/closed, but a later process cannot reconstruct an event
that storage never recorded. Guaranteeing closed-on-restart even for that case
requires a separately durable external authority or an explicitly approved
closed-by-default startup policy. Neither is claimed here. Do not treat a failed
acquire as a maintenance grant or restart to bypass its reported fault.

File syncs and boundary fault injection qualify **process-crash/reload logic**,
not machine power-loss/storage-controller durability. Directory metadata flush,
filesystem qualification, protected provenance/ACLs, noncooperating/privileged
writers and path-replacement attacks remain production prerequisites. Symlink
paths are refused, but these files are not an authenticated lease.

## Paths still outside the complete drain boundary

| Path | Current status |
| --- | --- |
| Batch claims over HTTP or negotiated WS RPC | Same `tryEnterClaim` mutex rejects new intents while held. Already-issued requests may finish and are counted. |
| Server queue/dispatch/reservation/retry transitions | Server remains independent; it may queue/dispatch work while this daemon is held. Nothing is cancelled, reassigned or deleted to manufacture a drain. Server-side enforcement/accounting is not implemented here. |
| Already-owned tasks, preparation and resource wait | Continue; maintenance does not retroactively stop their provider launches or external effects. Counts must reach zero before local quiescence. |
| Supervisor/Task Scheduler/process starts, respawn and other profiles | Not fenced by this local journal. A restarted daemon reloads held admission but still performs ordinary startup/registration; orphan descendants need independent qualification. |
| External installer, binary replacement and rollback writers | Not authorized or fenced by the in-process barrier. Worker transactions remain Production-disabled. |
| Health/forwarding/provider/WebSocket qualification and canary | Separate observations/approvals; neither a held journal nor zero local counts proves them. |
| Maintenance CLI/API/auth, renewals and protected operator recovery | Not exposed. Control must validate authority and host/profile/generation through the single daemon owner before production integration. |

PILOT-15 capacity work must not substitute capacity settings for these remaining
gates. Host shutdown/restart, external deployment and sign-in recovery still need
the approved executor/window and existing live-host permissions.

## Isolated checks

```powershell
Set-Location .\server
go test ./internal/daemon -run 'TestMaintenanceAdmission|TestTryEnterClaim|TestTrySetClaimBarrier|TestHandleUpdate|TestTryAutoUpdate|TestTrySelfReload' -count=1 -timeout 120s
```

Tests cover acquisition-versus-claim races, inflight handoff/resource-wait counts,
expiry/recovery/stale release, crash/reload, before/after persistence faults,
fence-only crashes, malformed/binding-mismatched journals, and the actual batch
poller against an isolated HTTP fixture. Held polling makes no server request;
after explicit release, the same poller resumes its claim path. No live queue,
profile, provider executable, daemon or host service is used.
