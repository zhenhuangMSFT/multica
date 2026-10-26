# Lifecycle source boundaries

The approved source extraction is split into independently reviewable scopes:

| Scope | Source / review |
| --- | --- |
| Optional fleet/OS policy | `extensions/runtime-manager`, version `0.1.0-dev`, standalone offline fixture runner; outside default frontend/Go builds. |
| Generic lifecycle contract | `server/pkg/lifecycle/v1`, ID `multica-lifecycle/v1`; strict platform-independent DTOs with unchanged existing command/result wire format. |
| State-owner enforcement | Backend `internal/daemoncontrol`, task admission queries/service and daemon local claim barrier/receipts; no external manager process needed for ordinary work. |
| Thin owner client | Core CLI/API diagnostic and lifecycle commands, including exact-owner read-only capability discovery. No provisioning/replacement executor. |
| Independent Copilot correction | [PR #4](https://github.com/zhenhuangMSFT/multica/pull/4), `b0aa908d8`; only the provider source/test. No net provider change remains in PR #3. |

The backend's capability response identifies its configured reservation fence,
not liveness, all-replica enforcement or a fresh remote observation. Daemon
coverage is unknown, supervisor launch unsupported, complete drain false.
Mutation still requires a fresh authenticated human exact owner across the
daemon-wide registration scope. Discovery, extension install or plugin consent
is not permission to launch, stop or deploy.

See [core admission/recovery](DAEMON-ADMISSION.md) for implemented semantics and
[the external architecture](EXTERNAL-RUNTIME-MANAGEMENT.md) for remaining
executor identity/privilege/transport, multi-party coordination and first-write
durability requirements. Source layout and public v1 types are implemented;
the privileged external service is not. The optional
`extensions/runtime-manager/executor` now contains a persistent launch/maintenance
state-machine library with fake transport/process tests and explicitly
unavailable production adapters, not a deployed executor. Optional offline plugin
source now lives in `extensions/runtime-manager/plugin`, using the existing manifest-v1
sidebar surface for plans/local proposal drafts only; it is not published,
installed or connected to a service. Production worker
transactions remain disabled; installed paths/startup are unchanged.

## Review and verification

The extraction uses separate commits for external source and generic hooks,
without history rewriting or a new repository/release pipeline. The original
provider commit is preserved on its own review branch; removing it from the
integration branch changes only PR composition, not that reviewed correction.

Run the external package's `Test-Package.ps1` explicitly with PowerShell 7.
It copies only that package into a unique temporary directory and runs all ten
fixture suites without core source.

Core tests from `server`, with only a task-isolated Redis fixture configured:

```powershell
go test ./pkg/lifecycle/v1 ./internal/daemoncontrol ./internal/daemon ./internal/cli ./cmd/multica ./cmd/server -run '^(TestLifecycleV1.*|TestMaintenance.*|TestRuntimeMaintenance.*|TestTryEnterClaim.*|TestTrySetClaimBarrier.*|TestHandleUpdate.*|TestTryAutoUpdate.*|TestTrySelfReload.*)$' -count=1 -timeout 180s -json
go vet ./pkg/lifecycle/v1 ./internal/daemoncontrol ./internal/daemon ./internal/cli ./cmd/multica ./cmd/server ./internal/service
go test ./internal/service -run '^$' -count=1
```

The last command compiles backend fixture tests only; it is **not** a database
race-suite execution. PostgreSQL reservation/retry tests remain source-owned at
the backend and their earlier evidence is historical, not rerun by that command.
Provider-only fake-child tests run separately on PR #4. No live provider,
CI-completion, full-suite, privileged service or production acceptance is implied.
