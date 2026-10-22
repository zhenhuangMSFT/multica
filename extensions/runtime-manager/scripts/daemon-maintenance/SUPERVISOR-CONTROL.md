# Supervised launch: proposed authority and transport contract

**Revised placement:** [optional external runtime management](https://github.com/zhenhuangMSFT/multica/blob/agent/multica-operator/4ba50ca7316e/docs/runtime-lifecycle/EXTERNAL-RUNTIME-MANAGEMENT.md)
is the current architecture recommendation. The broker, OS policy, enrollment,
deployment and journals proposed here belong to that separately versioned
extension, not core Multica. This document's authority/privilege choices remain
unapproved; a plugin package does not install or host the broker.

**Decision required; not implemented or enabled.** The existing maintenance
authority covers human requests, backend reservations and local daemon claims.
It does not authorize an external process to start children, and it cannot
serialize an OS process creation with maintenance acquisition. This proposal
does not change the three approved exact-owner authority choices or authorize
installation, a service/account change, migration or live maintenance.

## Why the existing boundary is insufficient

| Source | Implemented behavior | Missing launch guarantee |
| --- | --- | --- |
| `extensions/runtime-manager/scripts/windows-worker/Start-WorkerComponent.ps1` | Host/task guards, per-component file lock, duplicate-process/readiness checks, foreground child invocation. | No authenticated maintenance admission; process discovery followed by invocation is not atomic. Wrapper exit/crash has no durable child-creation receipt. |
| `extensions/runtime-manager/scripts/windows-worker/Install-WorkerLogon.ps1` | Separate root Tunnel/Daemon tasks; interactive owner logon, IgnoreNew, scheduler retries. | IgnoreNew covers a scheduler task instance, not another wrapper directory, task identity or orphaned child. Retries repeat the unfenced launch path. |
| `server/internal/daemoncontrol/admission.go` | PostgreSQL transaction fence before delivery; bound owner/scope/generation and results. | Its transaction ends before an external process starts. No launch reservation/acknowledgment or independently authenticated host executor exists. |
| `server/internal/daemon/maintenance_control.go` | Authenticated daemon polling and durable command intents/results. | The receiver must already be running. It cannot authorize its own first launch or supervise itself while stopped. |
| `extensions/runtime-manager/scripts/daemon-maintenance/WorkerSnapshot.psm1` | Read-only task/file and referenced child identity observations. | A snapshot is evidence, not a live process handle, protected provenance or authority to create a child. Unknown coverage remains unknown. |

Neither `runtime maintenance status`, `auth probe`, anonymous local health nor a
receipt boolean is a launch permit. Copying a daemon or saved owner credential
into a wrapper is not an acceptable bridge. Wrapping `Start-Process` with an
HTTP GET and a file lock would still leave the acquire-versus-launch race.
No existing route accepts the scoped replacement operation described below.

## Proposed decision: an independently authenticated host launch broker

Approve or reject this **new** authority/transport design before implementing
production launch adapters:

1. A reviewed Windows host broker, independent of the worker daemon, is the sole
   process creator for both supervised components. Proposed hosting is a
   separately approved Windows service, not another agent task or a helper
   borrowing an owner profile. It uses an enrolled, host-specific,
   non-exportable private key for an outbound mutually authenticated backend
   channel. Enrollment, rotation, revocation and protected journal/key ACLs need
   owner/platform provisioning; none are installed by this source proposal.
2. The broker's OS identity/privileges must permit launching into the configured
   owner's **existing interactive sign-in session**, with the exact owner SID
   and without storing that owner's password. A LocalSystem service using a
   reviewed interactive-token acquisition path is a candidate requiring
   separate explicit privilege/service approval and implementation review, not
   a permission assumed here. No signed-in owner session means no launch.
3. Backend human `Auth` plus `RequireHumanActor` and the existing exact-owner
   registration/membership checks remain the only authority for maintenance,
   recovery, release and replacement approval. The broker credential only
   claims/acknowledges backend-authorized work for its enrolled host; it cannot
   submit those human operations or change their owner/scope.
4. Logon/scheduler wrappers become non-authoritative wake/request clients. They
   carry only an installation/component reference, never credentials, executable
   argv or a replacement override. Local wake traffic cannot create a permit,
   reopen a gate or impersonate a human. There is no unauthenticated local
   mutation endpoint. Broker service control and any IPC ACLs require review;
   a same-owner SID, a caller PID or loopback origin alone is not human authority.
5. The exact human owner must explicitly enroll the immutable installation
   manifest and its standing ordinary-launch policy through backend human
   authorization. That policy permits only those approved components after
   sign-in while the shared gate is open. A local wake request merely asks the
   backend to evaluate that existing policy; it cannot grant or edit one.
   Release does not enroll an installation. Changed manifests and replacement
   exceptions require fresh human authorization.

This expands the trust model to a credentialed, persistent host executor. The
existing approvals for daemon control did not approve it. If no independent
broker identity/service is acceptable, retain the current startup behavior and
keep supervisor fencing unavailable rather than introduce a weaker bypass.

## Proposed typed control boundary

These are message contracts for review, **not existing API routes or flags**.
Transport mounting, enrollment and authorization tests must precede activation.
All messages use strict bounded JSON, explicit UUIDs, UTC deadlines and a
configured authenticated origin; no redirect, automatic mutation retry or
arbitrary executable/path/argv input is allowed.

| Message | Binding and effect |
| --- | --- |
| `LaunchRequest` | Unique request ID, installation ID, host identity, owner user/SID, profile, daemon, exact root task path/name, component, immutable approved release/config/definition digests, expected maintenance generation and broker incarnation. Reason is `initial`, `scheduler_retry`, `component_respawn` or `replacement`. |
| `LaunchReservation` | Backend-bound request plus registration digest, one-use reservation ID, command digest, monotonic launch epoch, generation, mode and expiry. Ordinary mode requires open maintenance; it is not an offline bearer grant. |
| `CloseLaunchAdmission` | Existing human maintenance command identity/owner/scope plus generation and launch epoch. Broker durably closes ordinary launches under the same local serializer used for creation, then accounts for every earlier reservation/intent/child before acknowledging coverage. |
| `LaunchObservation` | Bound request/reservation/incarnation, journal sequence, component and outcome (`denied`, `intent`, `created`, `observed_exited`, `outcome_unknown`). Child identity includes PID, kernel creation time, actual image identity/hash, owner SID/session, profile and containment identity. Missing identity/coverage is explicit unknown. |
| `ReplacementLaunch` | Separately authorized human command bound to current held holder/generation, broker incarnation/epoch, transaction, exact replacement digests and one component/attempt. It is consumable only while held; it never reopens ordinary launch admission. |

The backend must persist launch reservations and command intent in its
transactional repository under the same daemon serialization as maintenance,
not only in Redis. One unresolved attempt per installation/component prevents
duplicate requests from producing a second child. Changed payload under an
existing ID conflicts. Multiple affected registrations remain daemon-wide;
unknown broker enrollment or mixed/unresolved ownership refuses authorization.
Already launched components are not cancelled by a reservation query failure.

## Linearization, acquisition and delayed delivery

The broker must own a protected, durable per-installation journal and a
kernel-enforced single creator/serializer shared by **every** component launch
and maintenance transition. A mutex by itself is not durable authority. Neither
backend acceptance nor a reservation snapshot is permission to call the OS later.

1. Backend acquisition first persists its existing closed reservation gate and a
   new launch-close intent/epoch. It stops issuing ordinary launch reservations.
   The command remains pending supervisor coverage until the broker acknowledges
   the close; today's daemon result alone must not upgrade that claim.
2. Every broker launch serializes generation/epoch checks, durable launch intent,
   OS creation and durable child outcome with `CloseLaunchAdmission`. If launch
   wins, close records that child as pre-acquisition owned work. If close wins,
   the child must not be created. Acquisition waits for or explicitly reports
   unresolved creation; it cannot assert stopped/complete coverage instead.
3. Local admission closes and is synced before close acknowledgment. The
   backend validates request, epoch, generation, broker incarnation, complete
   component inventory and all unresolved attempts before recording supervisor
   coverage. A delayed ordinary permit cannot cross this local closed epoch.
4. The backend's existing daemon acquire/recover acknowledgment and the broker
   acknowledgment are separate required evidence. Failure of either keeps
   backend closure and reports pending/unknown coverage, never complete drain.
5. Release remains human-only, exact owner and current holder/generation.
   Reopening broker launch admission requires a **new backend-committed open
   epoch** bound to the current incarnation and consumed under the serializer.
   An old open decision or expired holder cannot reopen it. Stale local/backend
   generation mismatch requires explicit reconciliation; no highest-value guess.

Operations admitted before acquisition may complete after its request arrives;
they must be accounted for, not hidden. Acquiring maintenance does not kill
healthy children. Connectivity loss and expired responses deny new creation
without cancelling existing processes. No long-running SQL transaction is held
across an unbounded network call or OS launch.

## Durable child outcomes and duplicate wrappers

Before any OS creation, exclusively persist/sync a request-bound `intent`.
Create the child suspended under controlled handle inheritance, assign it to
the reviewed Windows containment mechanism, capture creation-time/image/SID
identity from process handles, and persist `created` before resuming. The local
serializer must cover the final gate check and resume as well as creation.
Persistence failures never become successful launch acknowledgments.

Suspension/job assignment alone does not close every crash gap: the broker can
die between OS creation and child recording/containment. A surviving intent
with incomplete process identity is `outcome_unknown`; do not launch another
copy, call it stopped, release based on it, or terminate a guessed PID. Startup
must reconcile through qualified OS containment/handles and exact identity, or
stay blocked for reviewed recovery. This provenance/crash-gap qualification is
required engineering, not a property established by this proposal.

Recorded child exit needs matching creation-time/image identity and an actual
exit observation. Missing process listing, access denial, stale heartbeat,
stopped scheduler task or PID reuse is not sufficient. Retain the original
identity, uncertainty and terminal evidence across broker restart. Journal
corruption, inaccessible storage, lost enrollment or unknown incarnation denies
creation and prevents a successful coverage acknowledgment.

Duplicate wrappers, alternate working directories, scheduler retries and manual
invocations all map to the same installation/component reservation and journal.
A duplicate replays the saved outcome or reports the unresolved original
attempt; it does not create/adopt/stop a child. An observed exit can make a new
ordinary attempt eligible only under a fresh authorized open epoch.

## Replacement daemon without ordinary respawn

The approved external maintenance executor requests a separately authenticated
`ReplacementLaunch` while the ordinary gate remains held. The backend repeats
human exact-owner authorization and binds the exception to current
holder/generation, transaction, broker incarnation/epoch and exact approved
replacement. The broker consumes the exception once under its creation
serializer and durable journal. It cannot be supplied through wrapper flags.

Replacement startup must preserve daemon/profile/runtime identity and load held
local admission before registration/polling; it must not run claims. An
unresolved generation or local receipt failure blocks startup/requires recovery,
not journal deletion. Starting a daemon is not permission for a provider canary
or release. A replacement exit/ambiguous launch consumes the attempt and
requires explicit new human authorization; scheduler retry cannot reuse it.
Connector replacement, if needed, requires its own equally bound operation.

## Proposed isolated acceptance matrix

No new launch adapter or race tests are implemented by this decision document.
After approval, use an injected clock, transactional repository, durable
journal, broker serializer and fake process factory; never owner credentials,
real provider CLIs, live tasks or actual host restarts.

| Scenario | Required assertion |
| --- | --- |
| Acquire versus launch/create/resume at each boundary | Either one accounted pre-close child or zero creations; close acknowledgment cannot outrun an unresolved launch. |
| Scheduler retry, initial start, component respawn | Same gate/epoch checks for both exact components; held denies every ordinary path. |
| Duplicate wrappers/IDs across directories or broker restart | Exactly one factory invocation, or unknown original outcome with no new invocation; changed payload conflicts. |
| Crash before/after intent, create, containment, receipt and resume | Durable replay or explicit unknown; unknown is never inferred stopped or auto-retried. |
| Expired/stale holder, reservation or old open epoch | No creation/reopen, including after restart; active children unaffected. |
| Backend timeout, delayed response, lost acknowledgment | Reject stale/incarnation-mismatched permission; retain pending closure and original ID/outcome. |
| Wrong owner/scope/host/profile/component/hash/session | No OS call, including a valid task token acting for the owner and an admin who is not the owner. |
| Invalid child identity, access denial, reused PID | Coverage unknown; no adopt/stop/second launch. |
| Replacement while held | One exact authorized replacement, no ordinary launch/retry, no admission release or provider canary implied. |
| Persistence/enrollment failure and concurrent broker instances | No success acknowledgment or unfenced launch; enforce one creator and preserve uncertainty. |

The approved service identity/IPC/credential design additionally needs
platform-specific authorization and crash-gap tests, not just fake-adapter
tests. First durable-write refusal/startup policy remains its separate owner
decision; this proposal does not choose or solve it.

## Delivery boundary

Existing live after-sign-in startup and the v1 scripts remain unchanged. Do not
install new wrappers or interpret this document as a launch receipt. Supervisor
fencing is unavailable until the new host-executor authority/transport is
approved, implemented and qualified.

Complete drain stays false; production worker transactions remain disabled.
Older replicas, other profiles, direct/external writers, trusted artifact/receipt
provenance, authenticated forwarding/readiness, canary and rollback acceptance
remain separate gaps. Deployment channel, per-host authentication and the
approved external window, PR-link repair, PILOT-15's parked state and existing
response monitors are unchanged.
