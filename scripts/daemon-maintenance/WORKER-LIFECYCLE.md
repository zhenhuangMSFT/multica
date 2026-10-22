# Worker lifecycle gate implementation

`WorkerLifecycle.psm1` implements the pure evidence-consumer and reconciliation
portion of the worker update/readiness contract. It has no scheduler, HTTP,
credential, process, registry or filesystem operations. Importing it cannot
administer a host. It is not an installer or maintenance authority.

```powershell
Import-Module .\scripts\daemon-maintenance\WorkerLifecycle.psm1
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerLifecycle.test.ps1
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerTransaction.test.ps1
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerLifecycle.integration.test.ps1 `
  -WorkerPackagePath .\scripts\windows-worker
```

The existing worker installer must not use a successful plan as rollout permission.
An approved external executor still needs real ownership/observation adapters and
the shared enforced admission boundary. Existing directory locks do not supply it.

## Implemented interfaces

All records are PowerShell dictionaries with exact, case-sensitive field names.
Unknown/missing fields, wrong primitive types and invalid identity bindings throw.
These are explicit errors, not ready/no-op fallbacks. Dates are UTC strings ending
in `Z`; JSON adapters must preserve strings instead of converting dates into
locale-formatted values. These schemas remain separate from the older strict
upgrade-evidence JSON schema.

| Function | Result |
| --- | --- |
| `Test-WorkerReadinessRecord -Record ... -Expected ... -ExitCode ...` | Validates bounded, fresh identity-bound dependency observations and state/exit/reason agreement. `dependencyReady` is not provider readiness or admission authorization. |
| `Test-WorkerMaintenanceDecision -Decision ... -Expected ...` | Requires matching holder/generation and fresh short-lived decisions. Drained additionally requires admissions blocked, zero inflight/active children, resource wait accounted for, preserved queues and suppressed respawn. |
| `Get-WorkerTaskReconciliation -Desired ... -Snapshot ... -Receipt ...` | Classifies exact owned no-op, blocked drift/collision/partial state, or stopped owned-update plan. No-op performs no writes and does not claim health. |
| `Get-WorkerTaskRollbackPlan -Previous ... -Candidate ... -Snapshot ...` | Plans recovery only for a pending transaction with adjacent trusted receipts and observed tasks/files matching known old/new hashes. Unknown drift or running/unknown children blocks recovery. It never guesses ownership or automatically retries a transaction. |

Reconciliation/rollback accept `-Maintenance` and `-ExpectedMaintenance`; all
functions accept an injectable `-Now` for boundary tests. Mutation plans always
return `authorizesMutation: false`, including when all supplied evidence agrees.
Maintenance results are checks of supplied assertions, not independently verified
leases. The readiness validator does not execute the collector or enforce its
deadline: `durationMs <= 10000` is only checked evidence. A production collector
must enforce the real deadline and output-size cap before decoding.

### Readiness record

Required fields:
`schemaVersion` (integer 1), `observationId` (UUID), `observedAtUtc`, `host`,
`profile`, `expectedWorkspaceId` (UUID), `ownerId` (UUID), `endpoint`, `state`,
`reasonCode`, `apiAuthenticated` (boolean), `forwardedEndpointVerified` (boolean),
`durationMs` (integer 0..10000).

The expected binding contains exactly `host`, `profile`, `expectedWorkspaceId`,
`ownerId`, `endpoint`. Host equality is ordinal case-insensitive; the other
bindings are exact. Only the approved loopback HTTP root endpoint is supported.
Evidence age is at most 45 seconds, never future-dated; 16 KiB is the serialized
record limit.

| State / exit | Allowed reason codes |
| --- | --- |
| `ready` / 0 | `ready`; both verification booleans must be true |
| `waiting_dependency` / 2 | `dependency_unavailable` |
| `reauth_required` / 3 | `authentication_expired` |
| `failed` / 1 | `access_denied`, `identity_mismatch`, `endpoint_mismatch`, `protocol_error`, `timeout` |

This adds explicit actual `ownerId` and `endpoint` bindings to the v2 proposal.
They prevent a valid-looking observation for another owner/origin from qualifying
the target. A collector classifies 401 as reauthentication, 403 as access denied,
connection/5xx as dependency delay, and redirects/HTML/malformed responses as
protocol failure. The validator cannot reconstruct those HTTP facts from JSON.

The CLI now includes a separate bounded owner-profile collector:

```powershell
multica --profile pilot-remote auth probe --expected-host WORKER-CANARY `
  --expected-endpoint http://localhost:8080 `
  --expected-owner-id 11111111-1111-4111-8111-111111111111 `
  --expected-workspace-id 22222222-2222-4222-8222-222222222222
```

Run only through the approved signed-in owner path after deploying a reviewed
binary. `auth probe` refuses task context, ambient credential/endpoint overrides,
wrong hosts and mismatched profile/workspace/endpoint bindings. It performs
`/api/me` and the expected workspace read with one ten-second context deadline,
no proxy, DNS resolution or redirects, bounded headers and 64-KiB JSON bodies.
Both accepted host spellings (`localhost` and `127.0.0.1`) connect directly to the
expected port on IPv4 loopback; IPv6-only listeners are not supported here. HTML,
duplicate top-level JSON keys, missing/mismatched identity and trailing JSON fail. It emits
only bounded metadata; no response bodies or token material, including in errors.
It classifies authentication, access denial, dependency loss and timeout separately.

Its JSON is an **API component observation**, not this module's full readiness
record: state `authenticated` means only those two authenticated reads passed.
Host/profile, UTC observation/expiry, identity, duration and endpoint are bound;
`forwardedEndpointVerified` and `authorizesMutation` remain false. A nonzero exit
also reports failure even if a JSON observation was emitted; CLI exit categories
are the existing standard categories, not the full-readiness protocol's codes.
Consumers must validate JSON and freshness and cannot treat process exit 0 as
tunnel/provider/maintenance readiness. This increment supplies stdout health data,
not an atomic health publisher or periodic monitoring service.

Independent tunnel/listener binding and actual scheduled-task execution-context
qualification remain required before aggregating full readiness. Neither an
anonymous health read nor an agent-task API read qualifies the owner context.

### Maintenance record

Expected fields are `host`, `profile`, `holder`, `generation` (positive integer).
Decision adds `schemaVersion: 1`, `observedAtUtc`, `expiresAtUtc`, `state`,
`newAdmissionsBlocked`, `inflightAdmissions`, `activeChildren`,
`resourceWaitAccounted`, `queuesPreserved`, `respawnSuppressed`.
Counts are nonnegative integers; gate flags are JSON booleans. States are `open`,
`held`, `draining`, `drained`. Lifetime and observation age are each at most 45
seconds; expiration is exclusive. Each real mutation must reacquire/revalidate a
decision immediately before acting; passing an old plan is insufficient.

### Receipt and snapshot

A trusted receipt has exactly `schemaVersion: 1`, `installationId` (UUID), `host`,
`profile`, `ownerSid`, `generation`, `releaseSha256`, `configSha256`, `tasks`.
Both task entries contain `taskPath`, `taskName`, `definitionSha256`. Only root-path
`MulticaWorker-PROFILE-Tunnel` and `MulticaWorker-PROFILE-Daemon` are accepted.
Hashes are 64 hex characters. Exact task-definition hashes include principal,
actions/argv, working directory, logon triggers, settings and enabled state.

The observed snapshot contains `host`, `profile`, `ownerSid`, `releaseSha256`,
`configSha256`, `tasks`, `journalPending` (boolean). Observed task entries additionally
contain `state` and `childState`, each `stopped`, `running` or `unknown`. Missing
tasks are an empty/subset array, not a fabricated stopped entry. A one-task pair
or pending journal requires deliberate recovery; an unreceipted pair is never
adopted. An update requires desired generation exactly previous+1 and a changed
release/config/task definition. Receipt validation by itself establishes no
provenance: an authorized loader must authenticate its protected source and hash
actual immutable files, not accept arbitrary caller-supplied receipts as ownership.

The rollback planner accepts only known whole old/new release+config pairs and
per-task old/new definitions. Missing tasks can be restored from trusted prior
definitions; unexpected live definitions cannot be overwritten. This handles a
partial registration conservatively but does not recover arbitrary file damage.
Unknown/tampered state requires manual review under the same maintenance authority.

## Wiring still required

PILOT-18 owns the real host adapter and supervisor integration. Integrate these
checks at the same points, not by duplicating the logic:

1. Load independently trusted receipt, actual exact-path tasks, immutable files and
   process identities. Preserve original XML for rollback. Task absence must be
   distinguished from access/read failure. Implement deterministic canonicalization
   and test actual scheduler serialization before issuing a receipt.
2. Establish actual core maintenance acquisition, durable lease/generation and
   suppressed launch/respawn across all writers. The receipt cannot act as a lease.
3. Journal before writes; register/re-read disabled task definitions one at a time,
   checking ownership and lease each time. Use stage-specific expected definitions
   for disabled/enabled forms; do not compare a disabled registration with an
   enabled-definition hash and then weaken equality to make it pass.
4. Wire the bounded API collector plus independent forwarding proof into complete
   readiness and atomic private health records; check observation freshness before
   launch and during supervision.
   Loss of readiness degrades health without killing healthy children or replaying
   external tasks. Provider canaries/WebSocket evidence remain separate.
5. Commit receipt only after approved canary verification. Restore exact prior task
   definitions/receipt on rollback; report journal/write/rollback failures. Never
   infer admission release from a verified journal phase.

`WorkerTransaction.psm1` now supplies simulation-only journal/receipt persistence
and the stopped-task transaction coordinator, consuming this module unchanged.
Its default Production mode refuses before store/adapters; Simulation is explicit,
not an OS sandbox for injected callbacks. Updates finish stopped and pending canary,
with the previous receipt still current. Explicit rollback verifies the old pair
before recording terminal state; neither operation releases admission.

The store atomically replaces one envelope with WriteThrough/Flush(true), an
integrity digest, revision checks and a cooperating-writer handle. It rejects
reparse-point paths. The digest does not authenticate a malicious writer; real
ACL/provenance checks remain required. Exception-boundary tests are not power-loss
qualification. Timestamp-preserving JSON decoding supports PowerShell 7.0+ and
rejects duplicate stored keys; no PowerShell upgrade is required on the pilot.

No trusted live scheduler adapter, protected production receipt loader, canary
finalization, full readiness aggregator, atomic health publisher or shared
drain/lease implementation is claimed. Those are engineering/qualification
dependencies, not merely owner approvals.

The integration test consumes the actual v2 installer Plan and launch-policy
helper, mapping two loopback configurations into this module's identity/ownership
checks. It never registers tasks or runs providers. Fake receipt/task hashes in
that test are fixtures, not scheduler canonicalization or live ownership evidence.
