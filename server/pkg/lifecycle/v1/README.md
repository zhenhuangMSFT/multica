# Generic lifecycle control v1

Contract ID: **`multica-lifecycle/v1`**. This public, platform-independent Go
package owns the typed command/delivery/result DTOs and strict bounded JSON
decoder used by backend, daemon and CLI. It has no dependency on Multica
`internal` packages or the optional runtime manager. No compatibility aliases
remain in `pkg/protocol`; internal callers import this package directly.

## Wire and authority

The command/result wire names, JSON field order, deadlines, 16-KiB bound and
strict required/unknown/duplicate/null handling are unchanged by extraction.
Existing persisted intents, Redis delivery/results and installed CLI requests
need no dual writes, migration or fallback. Contract versioning is additive
discovery; no version key is silently added to existing strict payloads.

Authenticated human routes:

| Method/path | Result |
| --- | --- |
| `GET /api/runtimes/{runtimeId}/maintenance/capabilities` | `MaintenanceCapabilities` for this backend and authorized daemon-wide scope. |
| `POST /api/runtimes/{runtimeId}/maintenance` | `MaintenanceRequest`; command operation is `status`, `acquire`, `recover` or `release`. |
| `GET /api/runtimes/{runtimeId}/maintenance/{requestId}` | Bound `MaintenanceRequest` and its optional result. |

The same human-only exact-owner check applies to discovery and mutation across
every runtime/workspace registration. Admin, plugin and managed-task identities
do not grant authority. Daemon delivery/report routes retain their own exact
daemon authentication; they do not authorize an external host executor.

Capabilities describe implemented configuration, **not readiness, permission,
global replica coverage or fresh daemon observations**. `backend_admission`
is supported only with the backend's fenced delivery repository configured.
Repository liveness and actual transition success are established by the
operation, not discovery. `daemon_admission` is currently `unknown` and
`supervisor_launch` is `unsupported`; no remote participant capability protocol
has been implemented. `complete_drain` is always false. Consumers must reject
incompatible contract IDs/unknown enums, validate scope/bindings and treat
missing/unavailable/unknown required coverage as unavailable, never open.
The thin owner CLI exposes the same read as
`runtime maintenance capabilities <runtime-uuid>`; it rejects transition flags
and validates the same strict typed response.

## Optionality

Normal Multica startup and work require no runtime-manager package, plugin
installation, executor process or release metadata. With no explicit hold,
missing/unavailable maintenance delivery does not block ordinary local claims.
Once held, omission/uninstall/outage/expiry cannot reopen admission; recovery
and release remain backend-authenticated generation-bound operations. Core
status reports only core-owned tasks/claims, not external-child coverage.

The external executor, OS privilege/enrollment, signed release packaging and
first-write durability policy are not implemented by these DTOs. Do not use a
capability boolean as approval to launch, replace or provision.

## Verification

Run `go test ./pkg/lifecycle/v1` from `server`. Adjacent tests cover strict
command/result input, old command byte compatibility, capability enum/version
rejection and malformed responses. `internal/daemoncontrol` owns authenticated
route/ownership tests; `internal/daemon` owns absent-extension and durable
claim-barrier tests; backend transaction races remain in `internal/service`.
