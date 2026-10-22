# Optional runtime-manager preview plugin

Manifest v1, version `0.1.0-dev`, one existing sidebar surface for web/desktop.
This is an **offline source package**, not a published or installed plugin.
It uses no hooks, resources, network endpoints, callback tokens, agent tools,
credentials, persistent storage or core changes. The current manifest-v1
validator requires at least one scope even for a static surface, so the manifest
declares the existing least-impact `storage:user` scope. This preview makes
**no storage calls**; it grants no task, issue, network or lifecycle scope.
Any future use of that per-member storage needs a separately reviewed behavior
change, not an assumption that installing this preview enabled persistence.

The single classic script displays absent/unavailable service status, static
prerequisite plans and local change-request drafts. Draft creation is not
submission or approval. The read-only draft can be copied manually; closing,
disabling or removing the frame discards it, with no OS operation, maintenance
release or lifecycle request. The only outbound host-bridge message is resize.
No plugin-install, disable or uninstall handler exists.

Installation is never evidence of manager/executor availability. No approved
endpoint exists, so the shipped surface always starts with manager/executor
missing, remote coverage unknown, supervisor launch unsupported, complete drain
incomplete and production transactions disabled. Fixture observations are used
only in isolated tests, never passed through the host bridge or loaded as live
responses. A core-only capability response is rejected as the wrong preview
contract, not upgraded into host readiness.

`runtime-manager-preview/v1` is a local fixture/draft format, **not** the proposed
privileged executor protocol or a new backend API. Future service connectivity
needs its own approved endpoint/authentication and separately reviewed response
schema. This package cannot bypass those decisions or the existing exact-owner
core authorization. Free text must use non-secret aliases/reasons; basic input
rejection is not a credential scanner or a promise to sanitize arbitrary secrets.
No actor identity is collected or claimed.

## Isolated tests

With Node 24, from this directory:

```powershell
node --test ui\main.test.cjs
```

Tests use the exact script with a bounded DOM/MessagePort fixture, not a real
browser or installed workspace. They cover status schema rejection, plans,
draft validation/rendering, malformed input, text-only output, bridge privilege
refusal and frame removal/remount. Network/storage/process boundaries throw if
called. No external dependency installation or frontend build is required.

Verify the actual manifest and classic script with Multica's existing bundle
parser, from the repository's `server` directory using its Go module:

```powershell
go test ..\extensions\runtime-manager\plugin\contract_test.go -count=1
```

That explicit external test is not part of default core builds/tests. It
packages a temporary in-memory archive and checks the real parser and declared
surface; it does not publish, install or invoke Multica platform APIs. The
frontend bridge itself is existing infrastructure, not reimplemented here.
No new general framework, scope or default workspace dependency is introduced.

For a browser-only visual fixture, open `ui/preview.html` as a local file.
It has no host bridge, reports that explicitly and uses the exact same script;
it is not part of the published manifest artifact. Do not serve it publicly or
interpret the rendered preview as an installed/connected runtime manager.
