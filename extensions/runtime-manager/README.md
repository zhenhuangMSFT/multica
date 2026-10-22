# Runtime manager (optional external source package)

This separately versioned source package owns fleet/OS policy, Dev Box
preparation, after-sign-in Windows startup, snapshots and simulated lifecycle
transactions. It is staged here for reversible extraction into a future
explicitly selected repository. It is **not** part of the server, daemon, Go
module, frontend workspace or default build. No remote repository, pipeline,
service installation or live configuration was created.

`extension.json` is this package's metadata, **not** a `multica.plugin.json`
manifest or a service installer. Version `0.1.0-dev` is an unreleased source
version, not a signed production release. Multica never reads this file during
startup or task execution. Copy this directory alone to review/run its isolated
tests; there are no imports of core source, private database queries or Go
internals. Runtime interaction uses supported CLI/API contracts, never saved
owner credential forwarding.

## Contents

| Directory | Owner / implemented scope |
| --- | --- |
| `extensions/runtime-manager/scripts/devbox` | Guarded provisioning/registration preparation and offline config mapping; cloud writes require separate approval. |
| `extensions/runtime-manager/scripts/windows-worker` | Guarded optional logon installer and existing direct-launch wrapper; not an atomic maintenance-aware supervisor. |
| `extensions/runtime-manager/scripts/daemon-maintenance` | Pure lifecycle policy, read-only snapshots, simulation-only transactions, evidence validation and external executor design. |

Generic reservation/claim fencing and authenticated owner control remain with
Multica's backend/daemon. The public core contract is
`server/pkg/lifecycle/v1` in the Multica source, identified as
`multica-lifecycle/v1`. Do not copy its state machines or import core internals
into this package. The independent Copilot correction is isolated in
[PR #4](https://github.com/zhenhuangMSFT/multica/pull/4), commit `b0aa908d8`
(only `server/pkg/agent/copilot.go` and its regression test). It is removed from
PR #3's net diff, is not a manager dependency and needs no extension to run.

The approved external-service boundary does not approve a host executor's
identity, privilege, credential enrollment or transport. That executor remains
**unimplemented**. A plugin UI, skill or MCP endpoint cannot host or authorize
it. Complete drain is false; production transactions are disabled. Existing
live startup paths are not relocated or upgraded by moving this source.

## Isolated verification

With PowerShell 7 on Windows, from this package directory:

```powershell
.\Test-Package.ps1
```

The runner copies this directory into a unique temporary directory without
Multica core, validates metadata and runs all ten offline fixture suites in
separate foreground PowerShell processes. It performs no installation, account
lookup for live execution, cloud mutation, task dispatch or daemon maintenance.
Failed suites stop with a nonzero exit; temporary copies are removed. Run this
explicitly; it is not connected to a startup service or release pipeline.

Source moves preserve sibling script paths. Existing installed tasks refer to
their original approved immutable deployment paths; **do not retarget them to
this checkout**. There is no compatibility launcher at the former repository
`scripts` paths. Installation/cutover remains a separate owner-approved change.
