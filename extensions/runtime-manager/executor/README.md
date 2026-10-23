# External executor source increment

**Implemented library and isolated adapters, not an installed host service.**
This optional source package owns launch serialization, durable attempt/replay
state, unknown-child reconciliation and one-use replacement. No core package
imports it. Injected certificate/message verification is implemented; production
transport and trusted enrollment/decision/human-approval sources, Windows process
creation, service hosting, protected storage and the external manager are unavailable.

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
alone. The optional `CertificateAuthority` adapter verifies signed decisions and
participant results against injected trust, channel and authoritative records;
see [its verification contract](#injected-authority-verification).
`UnavailableAuthority` and `UnavailableProcesses` remain the explicit defaults.
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
incarnation/epoch/generation are rechecked under it. After intent persistence and
after child persistence, live authorization is revalidated immediately before
create/resume: parent cancellation, request deadline, core observation freshness
and replacement hold expiry all apply. Authority and process adapters receive
contexts bounded by the earliest applicable deadline or the parent's remaining
budget, with timers released after each call. Adapters must honor cancellation
and recheck it at their OS side-effect boundary; the library cannot forcibly
interrupt an adapter that ignores its context.

The mutex spans intent,
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
Expiry/cancellation after consuming an intent does not refund its one-use
replacement exception, guess a child stopped or reopen admission, even when
create was refused before any OS call.
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

Receipt reload validates historical structure, exact core bindings and result
digests, not authorization at an invented future time. A proof valid when
accepted may have an older observation or a hold expiring before the request
deadline; neither prevents later reload. Live execution/replay still requires
fresh authentication and live deadlines. Historical records grant no new
launch, release or replacement authority and are not discarded on expiry.

An abandoned lock is never stolen by age/PID guessing; a crashed fixture/service
requires verified offline lock recovery. This favors refusal over duplicate
creators. Directory/power-loss durability, reparse-point attacks, hostile
same-account writers, cross-profile coverage and trustworthy process containment
remain unqualified. A production protected journal/locking adapter is required.
The local executor's sealed bootstrap does not change core daemon open-startup
policy or settle its first-fence-write decision.

## Injected authority verification

`CertificateAuthority` implements `Authority.Verify` without a listener, network
client, key loader or default system roots. It needs **all** of these trusted,
context-aware sources: approved active enrollment/revocation configuration,
completed TLS channel state, committed manager decision and, for maintenance,
a separate authenticated-human approval. There are no production source
implementations. Missing/unapproved configuration is unavailable, never an
allow-all fallback. The default adapters remain unchanged.

Enrollment fixes the exact binding/incarnation, manager URI and DNS name,
executor URI, distinct certificate SHA-256 pins and explicit root certificates.
Verification requires TLS 1.3, completed verified handshake evidence, matching
peer/message certificate, exact manager DNS/URI or executor URI, explicit
server/client authentication roles, valid pinned chains and nonrevoked peer/CA
fingerprints. No system trust, AIA/CRL/OCSP lookup, DNS lookup or network fallback
is used. Fixtures generate only ephemeral in-memory Ed25519 keys/certificates.
Other signature algorithms and production CNG integration are not implemented.

The subordinate `runtime-manager-executor/v1/authority` message uses strict
bounded JSON and Ed25519 over `SignedAuthority.SigningBytes()`: fixed-order
JSON containing contract, kind, request ID/digest, outcome digest, recipient
certificate fingerprint, issue time and expiry. Digests are SHA-256 of Go JSON
encoding of the existing typed `Request`/`Outcome`; the full request includes
the unchanged core proof, definition, transaction, scope and incarnation.
Digest and signing helpers return serialization errors; failed JSON encoding
never produces a digest or signing input. Outcome validation also rejects
unencodable child timestamps, including unknown outcomes, before signature
acceptance. Altering child or historical fields cannot preserve a malformed
outcome's signature by collapsing encoding failures to empty bytes.
Messages must expire at the request deadline, have at most one minute of
validity and never be future-issued. Enrollment, certificates and human approval
must cover that deadline, so a shorter credential cannot extend execution's
existing budget. Message certificates/signatures are transport fields, not
actor metadata or new lifecycle authority.

Committed ordinary-launch policy covers `launch` and generation-zero bootstrap
`open`; observation is participant delivery. `close`, held-generation `open` and
`replace` additionally require an independent exact-human-owner approval bound
to the **same request ID/digest** and expiry. A manager signature, participant
certificate, task token, plugin callback or actor string cannot replace that
record. Actual human authentication remains the future source's obligation;
fixtures do not qualify a backend integration.

`VerifyResult` independently checks the executor's client certificate, signed
outcome digest and existing `Outcome.Validate` after rechecking the committed
request authorization. The result signature is delivery provenance, **not** a
replacement grant, complete-drain attestation or proof of an actual OS action.
Historical flags are signed and stay historical.

Enrollment, committed-decision, human-approval and signed manager-decision checks
are endpoint-independent. Executor-side `Verify` additionally requests only the
`manager` peer state (manager server identity). Manager-side `VerifyResult`
requests only the `executor` peer state (executor client identity); it neither
calls request acceptance nor asks for the other endpoint's view of the TLS
connection. Fixtures include a real mutually authenticated TLS 1.3 handshake
over `net.Pipe`, using ephemeral keys with no network socket, and supply each
receiver only its own connection state.

Every verification reads current authoritative revocation/decision records;
there is no grant cache or in-memory nonce consumption. Exact live duplicates
remain eligible for the executor's durable historical replay, not another
launch. Changed bytes, revoked/expired decisions, delayed responses and old
incarnations are refused. Sources must return owned consistent snapshots and
honor bounded contexts; cancellation is rechecked after their return. Production
atomic revocation/source consistency and revocation during already accepted
local work remain unqualified. There is no claim of instantaneous revocation
of an in-flight launch or any new process cancellation.

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
$files = Get-ChildItem -LiteralPath ..\extensions\runtime-manager\executor -Filter '*.go' | ForEach-Object { $_.FullName }
go test @files -count=1 -timeout 90s -v
go vet @files
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
