# External executor source increment

**Implemented library and isolated adapters, not an installed host service.**
This optional source package owns launch serialization, durable attempt/replay
state, unknown-child reconciliation and one-use replacement. No core package
imports it. Production authentication, Windows process creation, service hosting,
protected storage, enrollment and the external manager are unavailable.

## Implemented contract

`runtime-manager-executor/v1` uses strict, bounded JSON decoding from public
`server/pkg/lifecycle/v1`. Core maintenance results are reused unchanged, rather
than reimplementing core holder transitions or accepting a `drained` boolean.
`Request.Transaction` references the existing `WorkerTransaction.psm1`
`journal.transactionId` UUID. This library does **not** execute that coordinator,
rewrite its receipt or relax its simulation-only guard.

Every request binds an installation, exact host/profile/daemon, owner UUID/SID,
selected owner session, registration digest, executor incarnation, generation,
epoch and UTC deadline (at most one minute). Launches carry one of the two
enrolled definitions (`daemon`, `tunnel`) with exact artifact, config and launch
digests. There is no shell/path/argv field, command runner, network listener,
credential reader or automatic OS discovery.

`Authority.Verify` must verify an authenticated, committed external-manager
decision; it is not implemented by checking actor metadata or the core proof
alone. `UnavailableAuthority` and `UnavailableProcesses` return explicit errors.
Tests supply fake authenticated decisions and process identities, never live
credentials or processes. There is no exported "allow all" production adapter.

| Operation | Serialized behavior |
| --- | --- |
| `open` | Requires a fresh authenticated epoch and exact generation. Generation-zero bootstrap requires separately enrolled ordinary-launch policy; later opening requires a healthy bound core release by the retained holder. Unknown attempts deny opening. Restart is locally sealed until a new decision; it never inherits an old open grant. |
| `close` | Requires a fresh higher generation, next epoch and healthy bound acquire/recover result. Persists closed admission, current holder, hold command ID and expiry. Existing children are accounted, not cancelled. A close receipt proves **only local launch closure**, not stopped children or complete coverage. |
| `launch` | Initial start, scheduler retry and respawn share the same open gate, exact epoch/generation and definition checks. One nonterminal attempt per component; unknown coverage anywhere denies further creation. |
| `replace` | Requires the current held proof/command/expiry, exact enrolled definition and transaction UUID. Consumes one `(transactionId, component)` exception durably **before creation**; failure, exit, restart or changed request ID cannot reuse it. Does not open ordinary admission or authorize claims/canaries. |
| `observe` | References the exact original attempt and definition. Only an adapter-proven child identity (PID + kernel creation time + image + SID + selected session + containment) can become running/exited. Absence, access denial, changed identity or unknown remains unknown. No PID guessing, adoption, termination or implicit retry. |

Authentication happens outside the local mutex; deadlines and current
incarnation/epoch/generation are rechecked under it. The mutex spans intent,
suspended creation, child recording, final deadline check and resume. A close
winning the serializer prevents creation; a launch winning it is recorded as
pre-close work before close can acknowledge. A delayed decision cannot bypass
the current closed epoch. The external manager's acquire must remain pending
launch closure until this acknowledgment: this library is **not wired into**
core acquisition, and a core snapshot alone is insufficient.

The journal consumes an intent before any process call, records exact child
identity before resume and commits the outcome after resume. Failed commits
latch the executor unavailable. A surviving intent/created/running attempt
becomes unknown on restart; only qualified observation can resolve it.
Results bind request digest, incarnation, generation and epoch; remote coverage
is always explicitly `unknown` (even when local fake children are accounted).
Replayed receipts are explicitly `historical`, never live launch-closure or child
attestation; an unknown replay returns `ErrUnknown` and never reruns a process.
Expired requests must be queried through
future authenticated manager history rather than replayed for execution.

## Persistence limits

`FileJournal` is a cooperating **source/test store**: exclusive create lock for
its full lifetime, sequential SHA-256-chained full-state records, file sync,
strict stored schemas and bounded retention (1,024 requests, 4 MiB per record,
16 MiB journal). Exhaustion requires reviewed archival; no automatic eviction.
Duplicate keys, missing fields, changed binding, torn records and digest/sequence
mismatch fail loading. SHA-256 detects corruption, **not malicious rewriting or
rollback**. No protected monotonic counter or Windows ACL is installed.

An abandoned lock is never stolen by age/PID guessing; a crashed fixture/service
requires verified offline lock recovery. This favors refusal over duplicate
creators. Directory/power-loss durability, reparse-point attacks, hostile
same-account writers, cross-profile coverage and trustworthy process containment
remain unqualified. A production protected journal/locking adapter is required.
The local executor's sealed bootstrap does not change core daemon open-startup
policy or settle its first-fence-write decision.

## Proposed production trust contract (not activated)

The concrete contract for review is in [TRUST-CONTRACT.md](TRUST-CONTRACT.md).
The remaining operational decision is whether to provision that external-manager
trust root and host service identity/session-launch privilege, with the specified
enrollment, IPC and journal protections. No source-layout/architecture approval is
being requested again. Until provisioned and qualified, the production adapters
stay explicitly unavailable.

## Isolated verification

Use the existing server Go module **only as a build harness** for the public v1
types (no `server/internal` imports). From `server`:

```powershell
go test ..\extensions\runtime-manager\executor\contract.go ..\extensions\runtime-manager\executor\executor.go ..\extensions\runtime-manager\executor\journal.go ..\extensions\runtime-manager\executor\executor_test.go -count=1 -timeout 90s -v
go vet ..\extensions\runtime-manager\executor\contract.go ..\extensions\runtime-manager\executor\executor.go ..\extensions\runtime-manager\executor\journal.go
```

This explicit external selection is absent from default Go/frontend builds.
No new module/repository, release pipeline or general plugin framework was
introduced. Tests use temporary journals, injected clocks, fake process factories
and fake authority responses. Deterministic races exercise both serializer orders,
blocked creation, delayed responses and concurrent duplicate requests; they are
not Windows API, mTLS, ACL, power-loss or full-system acceptance.

Complete drain and production transactions remain **false**. Upgrading approved
definition sets, actual stopped-task transactions, canaries/rollback, protected
enrollment, external-manager durable grant/revocation storage and production
Windows adapters remain separate increments. Existing startup wrappers and
offline plugin behavior are unchanged.
