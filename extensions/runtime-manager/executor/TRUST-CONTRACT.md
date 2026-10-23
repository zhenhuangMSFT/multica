# Proposed executor trust and activation contract

This makes the source adapter obligations concrete. It grants no account,
privilege, key, enrollment, listener, service installation or live operation.
The accepted external-service/minimal-core boundary remains unchanged.

## Identity and owner-session selection

The proposed Windows host is an independently installed `MulticaRuntimeManager`
service using LocalSystem plus a restricted service SID. It would need only the
reviewed privileges required for WTS owner-token acquisition and
`CreateProcessAsUserW`; no user password, cached PAT or copied owner profile token
is an enrollment mechanism. The trust/privilege choice is still an operational
approval, not granted by compiling this library.

An owner-approved installation enrolls the human account UUID, exact Windows
SID, machine identity, profile/daemon, component hashes and ordinary-launch
policy. Proposed session policy: require **exactly one existing active interactive
session** for that SID; no session, locked/disconnected session or multiple
eligible sessions means wait/refuse. Never log on the owner or choose the first
session. Bind the selected session ID to the executor incarnation and manager
decision; recheck SID, session authentication identity and state from the token
immediately before create/resume. A reused numeric session ID is insufficient.
The source `Binding.Session` and child checks enforce exact numeric equality;
only a future qualified Windows adapter can verify the underlying token.

The adapter must use suspended creation, controlled handle inheritance, a clean
allowlisted environment and a reviewed job/containment mechanism; it resolves
immutable, protected component definitions by digest, never shell input. No
adapter may extend the request, observation-freshness, hold or parent-context
budget. Check cancellation immediately before the OS call and return an
explicit unknown/error on timeout; cancellation is not proof of process exit.
The library rechecks live authorization after journal writes before create and
resume and retains consumed exceptions/unknown attempts on refusal. No
owner/service credentials enter argv, wrappers, tasks, plugin storage or child
environment. Before resuming a replacement daemon it must qualify the existing
daemon/profile identity and persisted local held generation. That check is not
implemented by the fake factory.

## External manager authentication and core ownership

The **external manager**, not Multica's scheduling backend, owns durable launch
decisions, open/close epochs, one-use replacement approval, operation history,
enrollment/revocation and fleet policy. No new core reservation tables/routes
are implemented. Core continues to own its atomic reservation/claim barriers
and the approved human-only exact-owner lifecycle authentication.

Proposed transport is outbound TLS 1.3 mutual authentication to one
owner-configured external-manager origin, with pinned deployment trust roots,
bounded requests and no redirects or automatic mutation retry. Enrollment uses
a host-specific non-exportable machine CNG key, accessible only to the service
SID and approved machine administrators. Certificates identify the enrolled
host/installation, not a human. No bootstrap endpoint/token is implemented.

`Authority.Verify` must authenticate that origin/peer and check its durable,
nonrevoked decision against **all request bytes** (including request ID, binding,
definition, transaction, deadline, epoch and incarnation). A certificate or
valid signature alone is insufficient. The manager must distinguish standing
ordinary-launch policy from per-operation exact-human maintenance/replacement
approval, recheck the approved core owner/registration scope, and bind the actual
core result. Task tokens, agent actor metadata, plugin callbacks and workspace
admin role cannot substitute for the human owner.

The manager first closes admission to new ordinary decisions, persists the
launch-close intent and waits for host closure/accounting. Its operation remains
pending/unknown until the host acknowledgment and independent core result are
both present. No network/SQL transaction is held while waiting for process
creation. A launch already locally serialized is pre-close work; it is not
retroactively cancelled. No "fully drained" result exists in this library.

Revocation denies subsequent `Verify`, including stale responses and bootstrap
opening. It does not kill an existing child or reopen maintenance. Binding/key
rotation needs a human-approved, generation-preserving handoff with fresh
incarnation and reconciliation; journal deletion/reinstallation is not rotation.
The external manager/transport are not implemented; the default
`UnavailableAuthority` still returns `ErrUnavailable`. The optional
`CertificateAuthority` now implements injected certificate/message verification,
not production credential loading, human authentication, enrollment or transport.
Its [source verification contract](README.md#injected-authority-verification)
specifies exact pins/URI/DNS roles, signed existing-contract digests, separate
human approval, bounded current-source checks and historical replay semantics.
Injected `ChannelSource` must supply actual completed, normally verified TLS 1.3
state from the future transport, never a caller-authored peer/actor claim.
Each receiver supplies only its actual peer: executor request acceptance checks
the manager server; manager result acceptance checks the executor client.
Shared authoritative-record verification does not require an opposite-endpoint
channel. Signed result/request digests and signing inputs must refuse all
serialization errors, including malformed typed timestamps, rather than hash
empty bytes or accept a signature over a failed encoding.
Injected configuration/record sources must be protected and authoritative,
including current revocation; fake fixtures are not production providers.

## Local IPC, journal and receipts

Proposed wrapper IPC is a local named pipe accepting only installation/component
wake hints. ACL: service SID and configured owner SID only; remote pipe access
denied. Even an allowed pipe caller cannot submit a lifecycle proof, change a
definition, create a grant, release a gate or choose session/argv. No pipe/server
is implemented. Wrappers remain unchanged and carry no executor credential.

Proposed `%ProgramData%` per-installation state/config directories disable
inheritance and grant write access only to the restricted service SID,
LocalSystem and explicitly approved machine administrators. The owner, daemon,
provider and plugin cannot write journal/definition/key material. Audit records
contain public bindings/digests and reason codes, not keys or credentials.
Actual DACL/reparse/hardlink protection, antirollback anchor, directory
flush/power-loss behavior and cross-instance kernel exclusion require a
separately qualified storage adapter; `FileJournal` is not that boundary.

Unknown children require attempt-bound containment/handle evidence, kernel
creation-time/image/SID/session validation and exact observed running/exit
status. Missing process listings, PID reuse, stopped scheduler tasks and stale
heartbeats remain unknown. Crash between creation and containment must remain
unknown unless the adapter can prove the exact child identity. No automatic
adoption, guessed termination or exception reuse is permitted.

## What must be decided before operational activation

Approve/provision the proposed service identity and session-token privileges
together with the external-manager trust root, enrollment/revocation authority,
IPC/DACL and protected journal location. Then implement and independently
qualify the production adapters, all launch participants and deployment channel.
Until then this is executable **source/test coordination**, not a functioning
deployed executor. Existing after-owner-sign-in startup is preserved; complete
drain, production transactions and rollout remain disabled.
