# Optional external runtime management: revised boundary

**Approved architecture; staged source extraction, not installation.** Runtime reliability
and fleet policy belong in an optional, separately released external service,
not Multica's backend, daemon or a new general plugin framework. A normal
Multica plugin can provide its UI, request hooks and skills; it cannot host the
privileged service or supply atomic lifecycle admission by itself.

This revises the placement in [SUPERVISOR-CONTROL.md](../../extensions/runtime-manager/scripts/daemon-maintenance/SUPERVISOR-CONTROL.md):
its launch serializer, journals, enrollment and OS executor belong to the
external extension. Its host-identity/service/privilege proposal remains
**unapproved**. The three approved backend choices remain: human-only exact
runtime-owner authority, daemon-wide same-owner scope with membership checks,
and backend-dependent mutation without an offline authentication bypass.

## Recommendation and dependency direction

Use the isolated `extensions/runtime-manager` source package until a separate
repository destination is selected. Its own `extension.json` version and explicit
standalone fixture runner do not participate in core startup or default builds.
The three fleet script trees and their tests have been physically extracted;
generic admission and this boundary document live under `docs/runtime-lifecycle`.
The independent Copilot correction is isolated at `b0aa908d8` in
[PR #4](https://github.com/zhenhuangMSFT/multica/pull/4), a two-file commit with no
fleet dependency, and removed from PR #3's net diff. No remote repository or
release pipeline was created.

The target remains a separate `runtime-manager` product/repository
with its own semantic versions, signed OS packages, release manifest, contract
fixtures and operator documentation:

```text
optional Multica plugin UI / request hooks / skills
                 |
        external manager API + durable operation store
                 |
        independently running per-host executor
                 |
        OS launch, containment, install, upgrade, rollback

external manager ---- versioned generic lifecycle API ---- Multica backend
external executor --- narrow lifecycle participant ------- Multica daemon
```

The manager owns Dev Box provisioning/enrollment, cloud budget/pool policy,
Windows logon/scheduler/service policy, tunnel mapping, source/artifact hashes,
owner-session execution, process lineage/containment, readiness/canaries,
deployment transactions and rollback. It may manage multiple supported runtime
products, not just Multica. It does not import `server/internal` packages, read
Multica database tables, overwrite daemon journals or implement task scheduling.
No new core-to-manager dependency is required for ordinary task execution.

The host executor survives the worker daemon being stopped. It must not run as
a task of that daemon or depend on a task-local MCP listener. An external
controller alone does not solve local creation races: the executor must
serialize launch/create/resume with gate closure and durably account for
uncertain children. That implementation and its identity are still gated.

## Verified extension mechanisms, not assumed capabilities

Inspected the existing source below at PR head `b9c4cff35`; these mechanisms are
already present independently of this PR's fleet changes.

| Mechanism and source | Can reuse for this extension | Cannot supply |
| --- | --- | --- |
| `server/pkg/plugincontract/manifest.go`; `server/internal/service/plugin_package.go` | Manifest v1, independently versioned immutable published bundles, explicit workspace install/upgrade, sidebar/issue/modal surfaces, HTTP/MCP hooks and skill resources. | No executable/service contribution or daemon lifecycle scope. Publishing stores frontend/resource files; author's backends are not hosted. |
| `server/pkg/plugincontract/bundle.go` | Zip with manifest-referenced UI/resource files; uploaded archive limit 2 MiB, decompressed referenced content 4 MiB. | Not an OS installer/package manager. Unreferenced files are dropped, not installed or executed. Do not hide a broker binary in a skill/UI artifact. |
| `server/internal/service/plugin_hook.go` | Signed HTTP invocations to declared `net:` destinations; UI/manual/agent/event/schedule triggers and bounded retries. Good for status, plans and change requests. | A signed hook or its actor field is not a human lifecycle grant. Async hooks cannot synchronously fence task reservation or OS creation. Schedule floor is five minutes, not a supervisor heartbeat. |
| `server/internal/service/plugin_mcp_transport.go`; `server/internal/daemon/plugin_hook_mcp.go` | Externally hosted MCP tools pinned by name/schema digest; HTTP hooks exposed as task tools via the existing broker. | Tool approval is not OS privilege. The task-local tool server dies with task/daemon; neither it nor a remote MCP server gets lifecycle permission automatically. |
| `server/internal/service/plugin_skill.go` | Install guidance as ordinary workspace skill text. | No background lifetime, authenticated control authority, protected receipt provenance or scheduler registration. |
| `server/cmd/server/router.go:registerPluginActionRoutes`; `server/internal/middleware/plugin_auth.go` | Existing `/v1` installation/callback-token actions for context, issue read/patch/comments and scoped storage. Session bridge retains user/resource checks. | No runtime lifecycle route in that Action API. Closed manifest scopes omit runtime maintenance. `mpi_`/`mpc_`, workspace install/admin consent and ordinary tasks cannot be substituted for the approved human owner. |
| `examples/plugins/deploy-sentinel/` | Existing pattern: separately hosted service, UI, agent-readable status and a hook that files a rollback request rather than performing rollback. | Its example is not a live deployment, service host or approved fleet identity. |

HTTP/MCP destinations require declared HTTPS endpoints and the existing outbound
network guard; production resolution must be public. Development-origin/CA
switches are not a way to expose an owner-only local pilot service or weaken
TLS. Do not add anonymous/public reachability to make a plugin work. If an
approved destination is unavailable, use a separately authenticated external
owner console/CLI and omit the optional plugin integration.

**Packaging choice:** publish a small, optional manifest-v1 UI/skill/request
bundle through existing settings; distribute the actual manager/executor as
separate signed artifacts. Their versions and compatibility ranges are distinct.
Installing/upgrading/uninstalling the UI never installs, starts, stops, upgrades
or removes the executor. No plugin is published or installed in this revision.
An initial plugin exposes only sanitized status/plan/request tools to agents;
privileged execution stays in a human-authorized manager workflow.

## Inventory of PR #3 and proposed disposition

Inventory baseline is merge base
`c24204f5baea577e57ecdc5f3e034f685767ccf9`, head `b9c4cff35`: **70 changed
files**. The rows below are the historical pre-extraction inventory, covering
every file once including tests and generated bindings. The three script trees
have since moved under `extensions/runtime-manager/scripts`, except these two
generic core documents under `docs/runtime-lifecycle`. Other dispositions remain
review guidance; no finished privileged service is claimed.

| Current PR file family | Revised ownership / refactor |
| --- | --- |
| `scripts/devbox/` (all six files) | Move provisioning, mapper, examples, tests and docs to external manager. No Azure/Dev Box dependency or credentials in core. |
| `scripts/windows-worker/` (all nine files) | Move startup/supervisor policy, examples, tests and health design to external executor package. Preserve live after-sign-in installations until separately approved migration; no automatic adoption. |
| `scripts/daemon-maintenance/` (all sixteen existing files) | Move lifecycle/snapshot/transaction/evidence modules, fixtures and operator docs to external manager. Split generic core-admission reference out of `DAEMON-ADMISSION.md`; retain source history and prior measured evidence as historical, not new acceptance. |
| `CLI_AND_DAEMON.md` | Keep only generic lifecycle API/CLI and standalone API-auth diagnostic documentation in core; link externally to optional fleet operations. |
| `server/pkg/agent/copilot.go`, `copilot_mcp_test.go` | Keep as independent provider correctness fix. Private-file managed MCP forwarding is needed without any fleet extension; separate review/commit series. |
| `server/cmd/multica/cmd_auth_probe{,_test}.go`; `server/internal/cli/owner_probe{,_test}.go` | Keep candidate standalone generic auth diagnostic, or split into independent PR. It proves authenticated API access only. Move host deployment/forwarding readiness orchestration to manager; no privileged launch permit. |
| `server/cmd/multica/cmd_runtime_maintenance{,_test}.go`; `server/internal/cli/maintenance{,_test}.go` | Retain only thin generic lifecycle owner commands/client. Move fleet orchestration and replacement CLI to external manager. Existing command names remain experimental until hook contract accepted. |
| `server/internal/daemon/daemon.go`; `maintenance_admission{,_test}.go` | Keep local generic claim barrier, durable generation/recovery and owned-work accounting with daemon, which owns claims/children. No scheduler, Windows or Dev Box logic here. |
| `server/internal/daemon/maintenance_control{,_test}.go` | Split: retain authenticated local lifecycle transition receiver and replay safety for those transitions. External deployment/replacement intent and OS-child journals belong to executor. Existing files currently hold core command receipts, not trusted host deployment provenance. |
| `server/internal/daemoncontrol/{control,store,admission}{,_test}.go`; `server/cmd/server/router.go` | Reduce to generic human-authorized lifecycle control, bounded transport/results and backend reservation fence. Keep core command deduplication only as transport; move standing fleet policy, host enrollment and rollout persistence out. Do not add a broker/service implementation to this package. |
| `server/internal/service/task.go`; `maintenance_admission_test.go`; `maintenance_admission_retention_test.go` | Keep reservation/final delivery/retry/retention fences and queue/race tests at state owner. Convert fixtures to generic lifecycle participants, not OS-specific fleet behavior. |
| `server/pkg/protocol/maintenance{,_test}.go` | Keep narrow versioned core lifecycle DTOs; no executable paths, OS SID privileges, cloud IDs or rollback artifacts. Extension envelopes and deployment DTOs live externally. Host/profile may remain opaque binding IDs, never core OS policy. |
| `server/pkg/db/queries/{agent,runtime,daemon_maintenance}.sql`; `server/pkg/db/generated/{agent.sql.go,runtime.sql.go,daemon_maintenance.sql.go,models.go}` | Keep server reservation/queue/ownership queries and generated bindings only. Manager must not depend on this schema. The inventory query is cross-workspace solely for same-owner authorization; no cross-owner fleet visibility. |
| `server/migrations/585_daemon_maintenance_admission.{up,down}.sql`; `586_daemon_maintenance_admission_index.{up,down}.sql` | Generic lifecycle persistence candidates, not external manager database. Review schema/version semantics before merge; never edit applied migrations or drop held state. No live application occurred. |

## Existing APIs versus minimum core hooks

Existing runtime listing/activity, registration/heartbeat, task APIs and
`auth probe` can support inventory, enrollment verification and diagnostics with
their normal permissions. Existing local daemon start/status/stop commands can
be invoked from an approved owner terminal; they are not an atomic maintenance
API. Runtime update's owner-or-admin gate is broader than the approved
exact-owner lifecycle authority; delete/cascade cancels/unbinds work and is not
a drain shortcut. No lifecycle authority should be inferred from these APIs.

The PR's new maintenance routes/fences are a **prototype of generic hooks**, not
an existing capability on unmodified upstream servers. Before exposing them to
an external manager, keep only the following small state-owner contract:

| Minimum generic hook | State owner and why an external-only implementation is unsafe |
| --- | --- |
| Discover lifecycle version/capabilities and opt-in participant bindings | Backend + daemon advertise actual enforcement, not version guesses. The extension cannot prove a replica or daemon implements a fence by looking at health/task counts. Discovery must not turn unsupported into open. |
| Human `status/acquire/recover/release`, immutable operation IDs and current generation | Backend authenticates the real human and enumerates every affected registration/workspace. The manager cannot recreate authorization from cached inventory or plugin admin rights. Daemon-wide scope stays exact-owner; status is bounded/sanitized. |
| Atomic backend reservation/dispatch/reclaim/retry/final-delivery fence | Backend owns these database transitions. A plugin hook after an event or a remote snapshot cannot lock their transaction boundary. Retain the existing queue-preservation and inflight reconciliation semantics. |
| Durable daemon claim closure/recovery and local owned-work observation | Daemon owns claim-inflight/provider/resource-wait transitions. External process counts cannot serialize local claims or accurately decide task ownership. Counts are local quiescence, not global drain. |
| Typed authenticated participant command/result binding | Backend/daemon must accept only correctly authorized state transitions and bound acknowledgments. Reuse existing bounded delivery where possible; don't create a universal plugin execution bus. External participant enrollment/credentials are a separate, unapproved contract. |

OS launch fencing itself stays **outside core**, at the external executor that
owns process creation. The manager combines core and executor acknowledgments
in its own operation store; core does not declare a host drained based on a
third-party `true`. Core may report its own local/backend quiescence and unknown
coverage, but never certify external children, supervisor shutdown, filesystem
replacement, forwarding or canary success.

No new runtime-management plugin scope, arbitrary callback executor, provider
framework or general extension loader is necessary. A future lifecycle action
bridge, if desired, needs separate narrow human-auth design; the existing plugin
Action API remains unchanged in this proposal.

## Versioned interfaces and authorization flow

Proposed external executor contract remains `runtime-manager/v1`, not an
implemented privileged service. The generic core contract is now implemented
as `multica-lifecycle/v1`, with public DTOs and strict decoding in
`server/pkg/lifecycle/v1`. Existing wire bytes are preserved; no internal alias,
dual write or persistence migration is needed. The core owns/version-controls
these DTOs; the external package has no dependency on private Go packages.

The current prototype maps human operations to
`POST /api/runtimes/{runtimeId}/maintenance` and bounded result reads to
`GET /api/runtimes/{runtimeId}/maintenance/{requestId}`. Existing daemon
`maintenance/claim` and `maintenance/{requestId}/result` delivery routes are
daemon-authenticated, not grants to the external executor. Reuse those
state-owner operations. `GET /api/runtimes/{runtimeId}/maintenance/capabilities`
and `runtime maintenance capabilities` now expose the v1 ID with the same
human exact-owner authorization. This backend reports its configured reservation
fence only; remote daemon coverage is unknown and supervisor launch unsupported.
External participant binding/enrollment is still unimplemented. Do not repurpose
daemon credentials for the manager or treat discovery as a permission/readiness
attestation.

- Capability response: supported major/minor, enforcement features, participant
  incarnation, immutable scope binding, observation time/deadline and coverage
  (`supported`, `unsupported`, `unknown`). Missing/mismatched major or any required
  feature refuses managed maintenance; an HTTP 200 or version string is not proof.
- Lifecycle operation: schema/operation UUID, exact human subject, owner/daemon/
  opaque host-profile binding, registration digest, expected generation,
  issue/deadline times and operation kind. Backend derives human identity from
  supported authentication, never trusts a body actor. Request IDs replay the
  same result only for identical payload; changed payload conflicts.
- Result: full request digest, participant/incarnation, observed generation,
  phase, bounded sanitized local counts, persistence/recovery faults and
  explicit known/unknown coverage. No credentials, raw commands or image paths.
  Unknown/expired/malformed is not stopped or drained.
- External deployment operation: separate transaction UUID, approved artifact/
  config/definition hashes, exact component policy, expected lifecycle holder/
  generation and one-use replacement authorization. It never becomes core task
  input or an executable supplied to a Multica API.

Use existing human-supported backend authentication from an approved owner's
console/CLI for core mutations, without copying saved credentials into wrappers
or plugin bundles. Plugin/manual hooks may create a **proposal**, not a grant.
An external human approval needs a separately reviewed identity/transport path
to bind a fresh core owner-authorized operation. Hook signatures, callback
tokens, agents, admin consent and actor metadata are insufficient.

If a credentialed manager/broker is later approved, it receives only scoped
participant delivery/report authority, not the power to impersonate the owner,
acquire/recover/release, rewrite scope or self-approve replacements. Separate
human authorization is required for a replacement while held. The proposed
launch epoch/serializer and unknown-child rules in the supervisor contract
still apply, now entirely within the external executor. Credential enrollment,
OS privilege, IPC and first-write durability decisions remain unapproved.

For a future managed replacement, the external coordinator must follow this
ordering, not a status-check-then-start shortcut:

1. Persist an operation bound to human-approved scope and generation. Request
   core acquisition and executor ordinary-launch closure. Neither acknowledgment
   alone constitutes complete maintenance.
2. Core serializes its reservation and claim barriers. Executor independently
   serializes closure with create/resume, accounts for pre-closure launches and
   persists any ambiguous child identity. Await both bound acknowledgments;
   preserve active tasks/queues and never stop a child on unknown coverage.
3. Only after positive quiescence/child reconciliation may an approved external
   transaction stop/replace. Replacement is a one-use, exact-artifact exception
   at the executor; core admission and ordinary launch remain held.
4. Verify replacement identity/readiness, then explicitly request owner-authorized
   core release and the separately authorized executor reopen. A partial release
   is recovery-required, not rolled back by guessing or retrying a mutation.
   Lost acknowledgments are reconciled using original IDs and current generations.

This is a multi-party saga, not distributed atomic commit. Persisting one
participant's closure does not establish qualified independent durability for
another participant's failed first write. If no durable intent exists, retain
that limitation rather than claiming the external architecture resolves it.

Core hooks and external schema versions negotiate explicitly. Additive optional
fields may be accepted only as documented; unknown critical enum/capability or
major fails closed. Retain strict bounded mutation decoding and UTC/deadline
checks; do not silently loosen today's strict JSON protocol to claim compatibility.
Upgrade the UI/service/executor/core independently only within a published
tested compatibility matrix. Schema/package downgrade cannot reset generations.

## Optional installation and unavailable behavior

| Condition | Required behavior |
| --- | --- |
| Extension never installed; no held core state | Ordinary Multica behavior and existing startup continue. No auto-enrollment, new network dependency, background jobs or mandatory fleet policy. Generic hooks are inert until explicitly used. |
| UI plugin absent/disabled/uninstalled | No UI/tool calls. External executor and any held gate are unaffected. Removing a plugin is not a service uninstall or lifecycle release. |
| Manager/executor absent or unreachable before opt-in | Show unsupported/unavailable; refuse managed rollout. Do not install it or claim supervision from scheduler presence. |
| Opted-in executor loses backend/manager connectivity | Deny new managed launches; preserve already-owned children and unknown outcomes. Existing held core gates stay closed, including across restart/expiry; no credential fallback. |
| Manager crashes after acquire or during replacement | Core retains its own durable gate; executor retains its own intent/child state. Inspect original IDs and explicitly recover current generation; never use plugin reinstall/uninstall as recovery. |
| Older core/replica lacks required hook | Planning/read-only diagnostics only; reject fenced maintenance. All affected replicas/participants must enforce it, not only the one answering discovery. |
| Extension removal with held or unresolved state | Refuse automated uninstall/reopen. Human-reviewed reconciliation/release through core and executor first; leave evidence if unavailable. Do not delete gate tables/journals. |

Optional means no global closed-by-default startup change for ordinary
installations. The enrolled executor's fail-closed launch policy and the
unresolved first-initial-write failure are distinct: no policy decision is
silently adopted here. Deployment changes and live migration remain gated.

## Strictly zero-core-change alternative

Against unmodified Multica, the external service can provision approved Dev
Boxes, install approved after-sign-in tasks, stage immutable artifacts, observe
documented API/CLI status and use OS supervision with its own launch journal.
An optional existing plugin can show status, file change requests and link an
authenticated owner to an external console. Its own creation/respawn serializer
can protect launches that actually go through it.

It **cannot** atomically stop server reservations/retries/final delivery or the
daemon's task claims through public status queries; nor prove drain while
dispatch continues. Stopping/deleting/unbinding a runtime is disruptive or
cancels work, not an equivalent substitute. It cannot recover a task token into
human authority or supervise external/legacy writers outside its containment.
Thus zero core change supports best-effort recovery and explicitly manual,
quiescent maintenance windows only, not unattended non-disruptive upgrade or
complete-drain guarantees. It must not reuse this PR's new hooks while calling
itself zero-core-change. This is a valid narrower offering if no generic core
hook changes are accepted.

## Migration/refactor plan, without executing it

1. The owner accepted the external boundary with generic core hooks and source
   extraction; the zero-core tier was not selected. Freeze OS/fleet expansion
   in core and keep PR #3 draft. A working privileged service is not delivered.
2. Source extraction into the local optional package is implemented. Select an
   explicit destination before creating an external repository/release pipeline.
   Publish versioned manager/executor contracts and package digests; keep private
   host inventory/credentials out. Do not provision resources or enable CI/tasks
   merely to perform this design review.
3. Split reviewable series: independent Copilot fix; optional generic auth probe;
   minimum backend/daemon lifecycle hooks and conformance fixtures; external
   policy/OS adapters. Reduce router/CLI surface to hook operations only. Keep
   transport receipts distinct from external deployment provenance; do not add
   compatibility shims/dual writers or change task queue ownership.
4. Finalize hook schema/discovery, supported identity boundaries and opt-in
   semantics. Translate existing maintenance tests into state-owner conformance
   tests. Review the prospective schema/migrations before merge. If ever applied
   elsewhere, introduce forward migrations and explicit state transfer; never
   rewrite applied history or remove held state. No live migrations occurred here.
5. Obtain separate executor identity/service/privilege and credential-transport
   approval before implementing privileged adapters. Develop fake-executor tests
   first. Build optional existing plugin package only for status/plans/requests;
   do not widen scopes or network policy just to reach the pilot.
6. After contract tests and production provenance/durability qualification,
   request a separate owner-approved enrollment/deployment window. Inventory
   legacy wrappers without adoption, preserve sign-in/daemon/runtime identity,
   and cut over only with explicit compatible participant evidence and rollback.
   Uninstall/migration must preserve queues, unresolved children and generations.

## Isolated contract tests required before extraction acceptance

These are acceptance requirements. The current extraction supplies standalone
offline fixture execution; privileged executor tests remain proposed until its
authority is approved. Consult the extraction handoff for measured results.

| Contract owner | Required isolated evidence |
| --- | --- |
| Plugin package/UI | Existing manifest parser accepts only UI/skills/request hooks; installing/disabling/uninstalling invokes zero OS calls; no lifecycle grant from callback/task/admin actor; MCP schema drift remains refused. Test UI from existing web/desktop surfaces, not a new framework. |
| Core backend hook | Exact-owner/mixed-scope rejection, atomic acquire-versus-reserve/reclaim/retry/finalize races, stale generation/release, retention, unavailable repository and missing capability; task attempts/queues preserved. No external service fixture needed for ordinary task execution. |
| Core daemon hook | Acquire versus local claim, inflight/resource-wait accounting, durable replay/crash/recovery, stale/expired results; no platform-specific process installer in fixtures. |
| External controller API | Core major/feature mismatch, plugin removed, duplicate/out-of-order operations, backend outage, lost acknowledgment and cross-owner denial; never infer host drain from core-only quiescence. |
| External host executor | Fake process factory and durable journal for launch/create/resume races, duplicate wrappers, creation uncertainty/PID reuse, delayed permissions and restart; one-use replacement while ordinary launch denied. Unknown coverage never becomes stopped. |
| Optionality/version matrix | No extension leaves ordinary behavior unchanged; unavailable enrolled service blocks new managed starts, not active tasks; missing hook refuses rollout; versions upgrade independently; removal cannot release a held gate. |

Prior `a03a00923` / `ef0b2f657` tests remain source-bound evidence for their
current implementation, not evidence that extraction, capability discovery or
supervisor fencing exists. Complete drain stays false and production worker
transactions stay disabled. Deployment/authentication/window, PR-link repair,
PILOT-15's parked state and existing response monitors are unchanged.
