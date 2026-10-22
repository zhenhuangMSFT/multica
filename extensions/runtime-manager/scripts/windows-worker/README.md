# Windows worker sign-in startup

Owner-operated starter kit for a remote worker connecting to an existing Dev
Tunnel and Multica profile. It does not enroll accounts, change credentials,
configure before-sign-in startup, restart services, or start tasks at installation.

This is a review candidate, not a qualified fleet deployment path. The shared
[lifecycle gates](../daemon-maintenance/WORKER-LIFECYCLE.md) and simulation-only
transaction coordinator do not enable this installer as a production updater.
Use the [combined canary contract](../daemon-maintenance/FLEET-CANARY.md) before
requesting installation/cutover approval. Preserve existing server-side tasks.

## Prepare

Copy this directory to a durable, owner-controlled local directory. Never register
tasks against a disposable agent workdir. Only the owner and administrators should
be able to modify its scripts/configuration. Keep the directory while tasks exist.
Copy pilot-remote.example.json to worker.json and verify every field against the
existing deployment. Preserve the existing workspace root and profile identity.
The profile must already target the approved loopback server endpoint. Authenticate
normally as the owner; do not copy account credentials or put secrets in this JSON.

The supervisor explicitly passes both `--no-auto-update` and `--no-auto-reload`,
matching the fleet bootstrap policy. Include `WorkerPolicy.ps1` in the release.
Install/Remove (including WhatIf) and supervisor launch reject a DeviceName that
does not match the actual computer name, ignoring case. Plan remains portable and
does not establish target-host readiness.

This source-only revision closes those two gaps. Authenticated readiness, shared
maintenance support and owned-version reconciliation remain unimplemented; see
`RECONCILIATION-HEALTH.md` for the proposed interface and remaining tests. The
directory lock is not a drain. Do not cut over using this candidate alone.

```powershell
.\Install-WorkerLogon.ps1 -ConfigPath .\worker.json
.\Test-WorkerLogon.ps1
.\Test-WorkerPolicy.ps1
```

## Owner installation

Use elevated PowerShell 7 under the configured owner. Managed task sessions are
intentionally rejected; do not remove environment variables to evade this boundary.
Establish an approved maintenance owner before applying. All cooperating installers
must use this same directory: its exclusive file handle serializes them, but does
not lock out manual Task Scheduler changes, other directories or other launchers.
The guard is not authorization or a machine-wide drain.

```powershell
.\Install-WorkerLogon.ps1 -ConfigPath .\worker.json -Mode Install -WhatIf
.\Install-WorkerLogon.ps1 -ConfigPath .\worker.json -Mode Install
```

Existing task names cause refusal instead of overwrite. Failure registering the
second task removes the first new task. Neither starts immediately. Tasks use
AtLogOn and Interactive, limited privileges, no password or execution deadline,
and one-minute restart intervals with 999 retries. The daemon retries if the
connector listener is not ready; TCP readiness is not authenticated readiness.

Per-component file locks prevent competing wrappers. The connector refuses any
existing listener on the configured port. The daemon refuses an existing explicit
profile daemon. These checks cannot exclude races with unrelated launchers; retain
the Multica CLI's own identity protections. No existing process is terminated.

Output goes to logs/Tunnel.log and logs/Daemon.log, each rotated after 5 MiB to
one prior file. Keep logs local because application output can contain account or
service information. A clean exit also triggers restart. A hung process requires
operator investigation; this kit does not terminate it or certify the connector's
in-process reconnection behavior.

## Cutover and acceptance

Review before reconciling old startup. In the approved maintenance window, export
the exact generic Multica Run entry, verify its command, and remove only that
redundant entry before testing sign-in. Restore it if cutover fails. This remains
an operator step: the installer never deletes a registry entry by a guessed name.
Preserve other profiles and the upstream server/tunnel-host startup tasks.

Drain/checkpoint work before sign-out, reboot or upgrade. Test the new tasks by
starting them only after the old processes are drained in the maintenance window,
then test reboot followed by sign-in on each required host. Require:

- No repeated failure loop and exactly one intended connector and daemon.
- Authenticated Multica access, expected runtime IDs, and a disposable provider job.
- Recovery from delayed upstream availability and a controlled connector interruption.
- Eligible interrupted-job retry with saved workspace/context and no duplicate effects.
- A separately controlled upgrade/rollback preserving identities and workspaces.

Registration alone does not prove these behaviors. Downtime until sign-in is
expected. Signing out differs from locking an existing session.

## Rollback

After draining, stop the two exact task names and account for their child processes.
Never kill every multica.exe process. Current removal refuses running tasks and
checks a description marker only; that is not sufficient ownership evidence for
fleet reconciliation. The design requires stronger checks before rollout:

```powershell
.\Install-WorkerLogon.ps1 -ConfigPath .\worker.json -Mode Remove -WhatIf
.\Install-WorkerLogon.ps1 -ConfigPath .\worker.json -Mode Remove
```

Restore the exported legacy startup entry and approved launcher. Logs/configuration
are retained. Do not delete the directory until its tasks and children are gone.
