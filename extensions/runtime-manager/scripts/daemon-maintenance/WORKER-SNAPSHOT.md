# Read-only worker snapshots

`WorkerSnapshot.psm1` adds the scheduler/file/process observation side of the
[lifecycle contract](WORKER-LIFECYCLE.md). It never registers, enables, starts,
stops or deletes tasks/processes, writes receipts, reads credential caches,
acquires maintenance or accepts a canary.

`Read-WorkerHostSnapshot` requires a clean owner terminal on Windows and explicit
expected host, owner SID, profile, release/config paths and SHA-256 hashes.
It refuses managed-task/ambient Multica identity overrides. The host name and
current Windows owner SID must match before files or scheduler state are read.
The profile binds the two exact root task names; a task with another principal
or an inconsistent root URI fails rather than being adopted.

```powershell
Import-Module .\scripts\daemon-maintenance\WorkerSnapshot.psm1

# Synthetic values only: substitute the separately approved private bindings.
$binding = @{
  host = 'WORKER-CANARY'; profile = 'pilot-remote'; ownerSid = 'S-1-5-21-123'
  releasePath = 'C:\ApprovedRelease\multica.exe'
  configPath = 'C:\ApprovedRelease\worker.json'
  releaseSha256 = 'A' * 64; configSha256 = 'B' * 64
}
$observation = Read-WorkerHostSnapshot -Binding $binding
```

The example is an owner-operated read, not a deployment instruction or evidence
that these bindings/files are authentic. Do not run it from a managed task by
clearing the guard variables.

## Scheduler and definition hashes

The adapter uses Task Scheduler COM `Schedule.Service`, connects locally and
opens only the root folder. It calls `GetTask` for
`\MulticaWorker-PROFILE-Tunnel` and `\MulticaWorker-PROFILE-Daemon`, never wildcard
search/adoption. Only **FILE_NOT_FOUND (`0x80070002`) from that exact lookup**
means missing. Folder/connection/export/access and all other lookup errors
propagate. Names/paths and exported principal SID must match exactly.

The original XML **string** returned by the scheduler is preserved unchanged in
`definitions[].originalXml`, separate from the strict lifecycle snapshot. This
is not an original-byte/encoding archive or a protected rollback store. XML can
contain private paths/action arguments: keep the observation private; do not
publish raw XML or use it as trusted receipt provenance.

`Get-WorkerDefinition` exports the same pure hashing routine for fixture/review
use. The `worker-task-xml-v1` SHA-256 input is a versioned token sequence:

1. Start with literal `worker-task-xml-v1`.
2. Append each token as its invariant decimal UTF-16 code-unit length, `:`, and
   the token. An element starts with `element`, namespace URI and local name,
   then attribute count and ordinal-sorted expanded-name/value pairs. Attribute
   expanded names are `{namespaceURI}localName`; namespace declarations are
   excluded.
3. A leaf appends `text` and its XML-decoded `InnerText`; an element with children
   appends the child element sequences in their original order. Finish each
   element with `end`. Encode the complete string as UTF-8 before SHA-256.

XML declarations, namespace prefixes, attribute order, comments and inter-element
indentation do not change the hash. CDATA/entity text uses XML-decoded values.
Leaf whitespace, element order, every other attribute and all definition fields
remain significant: principal, actions/argv/working directory, triggers, settings
and **explicit `Settings/Enabled`**. Scheduler `Enabled` must agree with XML.
Missing/duplicate required fields, DTDs, processing instructions, mixed content,
oversized XML and excessive depth fail.

This is conservative structural equality, **not** universal Task Scheduler
semantic normalization. Implicit defaults, boolean lexical variations and
reordered elements are not silently normalized. Actual scheduler export/
registration round trips on supported Windows versions still need qualification
before issuing any trusted receipt. Existing fixture-only hashes cannot be
adopted as real definition hashes.

Tasks are captured twice; pair appearance/disappearance, definition hash, enabled
or scheduler-state changes during collection fail. This detects some races but
does not create an atomic scheduler snapshot or fence later writers.

## File and process observations

Release/config files are opened read-only with `FileShare.Read` and retained
until collection finishes, preventing cooperating writes/deletion through those
handles during observation. Paths must be fully qualified local drive files;
UNC/device/alternate-stream and reparse-point ancestry are refused. Missing,
inaccessible or sharing-locked files raise errors, not fabricated hashes. Actual
hashes are returned in `snapshot`; `filesMatchExpected` separately reports
comparison with the supplied expected pair. Drift is preserved for lifecycle
classification, not replaced with the expected hashes.

Process inspection accepts optional explicit `ProcessReferences` entries:
`taskName`, positive `pid`, UTC `creationTimeUtc`, absolute `imagePath` and
`imageSha256`. Task names must belong to the same profile/pair, and duplicate PID
references fail. These are candidate tracking inputs, **not** authenticated
supervisor receipts or a proved parent/child relationship.

The live reader obtains local `Win32_Process` observations, inspecting owner SIDs
only for referenced PIDs. PID, creation time, executable path, current owner SID
and the current image file's SHA-256 must agree to classify a referenced process
as `running`. PID reuse, missing/incomplete identity and image drift become
`unknown` with explicit reason codes. Access/CIM/image-file failures propagate.
Command lines and unrequested accounts are not collected. Observed creation/
image/owner metadata is retained for private review; known running references
remain visible even when their scheduler task has disappeared.

A disk image hash does not authenticate the bytes already mapped into a process.
This increment does not establish a complete descendant census or track orphan
children across supervisor exit. Consequently **child absence is never inferred**:
every present task has `childState: running` when a referenced identity matches,
otherwise `unknown`. Disabled/Ready scheduler state can produce task
`state: stopped`, but does not make its children stopped; queued/unknown scheduler
states remain unknown. Empty PID lists, an absent/reused PID, or a stopped task
are never a drain proof. `childCoverage` is always `unproven`.

## Coordinator boundary and qualification

`observation.snapshot` fits the existing lifecycle/coordinator `Snapshot`
callback schema; XML/process details stay in the outer observation. Its
`journalPending: false` is only an observation placeholder: the existing
transaction coordinator replaces it from its own durable journal.

The fixture integration exercises this snapshot through the unchanged shared
policy and simulation coordinator: unknown children refuse an update before
maintenance acquisition, task writes or journal mutation. Production execution
still refuses before snapshot callbacks. A diagnostic snapshot is not a
production mutation adapter, receipt authentication, lease, forwarding proof, health
publisher or canary acceptance.

Remaining qualification includes actual scheduler serialization, supported
owner-session COM/CIM permissions, protected immutable material/provenance, and a
durable fenced child tracker tied to launch/admission and respawn. The adapter
does not keep file handles after return or defend against privileged path races/
hostile writers. Shared admission/drain and production conditional execution are
still required separately.

```powershell
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerSnapshot.test.ps1
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerTransaction.test.ps1
pwsh -NoProfile -File .\scripts\daemon-maintenance\WorkerLifecycle.test.ps1
```

Tests use isolated scheduler/CIM callbacks, XML fixtures and task-local files,
including real read-sharing and junction rejection. They do not query/register
live tasks, inspect live processes, use owner profiles or invoke providers.
