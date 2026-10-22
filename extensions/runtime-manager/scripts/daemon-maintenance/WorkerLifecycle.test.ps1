#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'WorkerLifecycle.psm1') -Force
$now = [DateTimeOffset]::Parse('2026-10-22T06:30:00Z')
$passed = 0
function Check([bool]$Condition, [string]$Name) {
    if (-not $Condition) { throw "FAIL: $Name" }
    $script:passed++
}
function Reject([scriptblock]$Body, [string]$Pattern) {
    $message = ''
    try { & $Body | Out-Null } catch { $message = $_.Exception.Message }
    Check ($message -match $Pattern) "Expected $Pattern; got $message"
}
function New-Readiness {
    @{
        schemaVersion = 1
        observationId = '11111111-1111-4111-8111-111111111111'
        observedAtUtc = '2026-10-22T06:30:00Z'
        host = 'TEST-WORKER'
        profile = 'pilot-remote'
        ownerId = '22222222-2222-4222-8222-222222222222'
        expectedWorkspaceId = '33333333-3333-4333-8333-333333333333'
        endpoint = 'http://localhost:8080'
        state = 'ready'
        reasonCode = 'ready'
        apiAuthenticated = $true
        forwardedEndpointVerified = $true
        durationMs = 100
    }
}
$expected = @{}
$r = New-Readiness
foreach ($key in @('host', 'profile', 'ownerId', 'expectedWorkspaceId', 'endpoint')) { $expected[$key] = $r[$key] }
Check ((Test-WorkerReadinessRecord $r $expected 0 $now).dependencyReady) 'ready record'
Check (-not (Test-WorkerReadinessRecord $r $expected 0 $now).authorizesAdmission) 'readiness never releases admission'
foreach ($key in @('host', 'profile', 'ownerId', 'expectedWorkspaceId', 'endpoint')) {
    $r = New-Readiness
    $r[$key] = if ($key -in @('ownerId', 'expectedWorkspaceId')) { '44444444-4444-4444-8444-444444444444' } else { 'wrong' }
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'mismatch'
}
foreach ($key in @('apiAuthenticated', 'forwardedEndpointVerified')) {
    $r = New-Readiness
    $r[$key] = $false
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'Ready requires'
    $r[$key] = 'true'
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'boolean'
}
foreach ($time in @('2026-10-22T06:29:14Z', '2026-10-22T06:30:01Z', '2026-10-22T06:30:00+00:00')) {
    $r = New-Readiness
    $r.observedAtUtc = $time
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'stale|timestamp'
}
$r = New-Readiness
$r.observedAtUtc = '2026-10-22T06:29:15Z'
Check ((Test-WorkerReadinessRecord $r $expected 0 $now).dependencyReady) 'exact 45-second freshness boundary'
foreach ($duration in @(-1, 10001, 2.5, '100')) {
    $r = New-Readiness
    $r.durationMs = $duration
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'integer'
}
foreach ($case in @(
    @{ state = 'waiting_dependency'; reason = 'dependency_unavailable'; exit = 2 },
    @{ state = 'reauth_required'; reason = 'authentication_expired'; exit = 3 },
    @{ state = 'failed'; reason = 'access_denied'; exit = 1 },
    @{ state = 'failed'; reason = 'protocol_error'; exit = 1 },
    @{ state = 'failed'; reason = 'timeout'; exit = 1 }
)) {
    $r = New-Readiness
    $r.state = $case.state
    $r.reasonCode = $case.reason
    $r.apiAuthenticated = $false
    Check (-not (Test-WorkerReadinessRecord $r $expected $case.exit $now).dependencyReady) 'nonready never launches'
    Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'mismatch'
}
$r = New-Readiness
$r.extra = $true
Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'Unknown'
$r = New-Readiness
$r.Remove('ownerId')
Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'Missing'
$r = New-Readiness
$r.state = 'READY'
Reject { Test-WorkerReadinessRecord $r $expected 0 $now } 'mismatch'
$r = New-Readiness
$r.reasonCode = 'x' * 16384
Reject { Test-WorkerReadinessRecord $r $expected 0 $now } '16 KiB'
$r = New-Readiness
$r.durationMs = 10000
Check ((Test-WorkerReadinessRecord $r $expected 0 $now).dependencyReady) 'exact 10-second evidence boundary'
foreach ($endpoint in @('http://localhost:8080/api', 'https://api.example.invalid', 'http://localhost:8080/?key=x', 'http://localhost:0')) {
    $r = New-Readiness
    $r.endpoint = $endpoint
    $e = $expected.Clone()
    $e.endpoint = $endpoint
    Reject { Test-WorkerReadinessRecord $r $e 0 $now } 'loopback'
}

function New-Maintenance {
    @{
        schemaVersion = 1; host = 'TEST-WORKER'; profile = 'pilot-remote'; holder = 'external-executor'
        generation = 4; observedAtUtc = '2026-10-22T06:30:00Z'; expiresAtUtc = '2026-10-22T06:30:30Z'
        state = 'drained'; newAdmissionsBlocked = $true; inflightAdmissions = 0; activeChildren = 0
        resourceWaitAccounted = $true; queuesPreserved = $true; respawnSuppressed = $true
    }
}
$maintenanceExpected = @{ host = 'TEST-WORKER'; profile = 'pilot-remote'; holder = 'external-executor'; generation = 4 }
$m = New-Maintenance
Check ((Test-WorkerMaintenanceDecision $m $maintenanceExpected $now).drained) 'complete drained decision'
foreach ($key in @('newAdmissionsBlocked', 'resourceWaitAccounted', 'queuesPreserved', 'respawnSuppressed')) {
    $m = New-Maintenance
    $m[$key] = $false
    Check (-not (Test-WorkerMaintenanceDecision $m $maintenanceExpected $now).drained) "blocks $key"
}
foreach ($key in @('inflightAdmissions', 'activeChildren')) {
    $m = New-Maintenance
    $m[$key] = 1
    Check (-not (Test-WorkerMaintenanceDecision $m $maintenanceExpected $now).drained) "nonzero $key"
}
$m = New-Maintenance
$m.generation = 3
Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'generation mismatch'
foreach ($key in @('host', 'profile', 'holder')) {
    $m = New-Maintenance
    $m[$key] = 'wrong'
    Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'mismatch'
}
$m = New-Maintenance
$m.activeChildren = -1
Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'integer'
$m = New-Maintenance
$m.respawnSuppressed = 'true'
Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'boolean'
$m = New-Maintenance
$m.observedAtUtc = '2026-10-22T06:29:14Z'
Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'stale'
foreach ($expiry in @('2026-10-22T06:30:00Z', '2026-10-22T06:31:00Z')) {
    $m = New-Maintenance
    $m.expiresAtUtc = $expiry
    Reject { Test-WorkerMaintenanceDecision $m $maintenanceExpected $now } 'invalid lifetime'
}
foreach ($state in @('open', 'held', 'draining')) {
    $m = New-Maintenance
    $m.state = $state
    Check (-not (Test-WorkerMaintenanceDecision $m $maintenanceExpected $now).drained) "$state not drained"
}

function New-Receipt([int]$Generation = 1) {
    @{
        schemaVersion = 1; installationId = '55555555-5555-4555-8555-555555555555'
        host = 'TEST-WORKER'; profile = 'pilot-remote'; ownerSid = 'S-1-5-21-1000'; generation = $Generation
        releaseSha256 = 'a' * 64; configSha256 = 'b' * 64
        tasks = @(
            @{ taskPath = '\'; taskName = 'MulticaWorker-pilot-remote-Tunnel'; definitionSha256 = 'c' * 64 },
            @{ taskPath = '\'; taskName = 'MulticaWorker-pilot-remote-Daemon'; definitionSha256 = 'd' * 64 }
        )
    }
}
function New-Snapshot {
    $receipt = New-Receipt
    $tasks = @($receipt.tasks | ForEach-Object { $t = $_.Clone(); $t.state = 'stopped'; $t.childState = 'stopped'; $t })
    @{
        host = $receipt.host; profile = $receipt.profile; ownerSid = $receipt.ownerSid
        releaseSha256 = $receipt.releaseSha256; configSha256 = $receipt.configSha256; tasks = $tasks; journalPending = $false
    }
}
function Plan($Desired, $Snapshot, $Receipt = (New-Receipt), $Maintenance = (New-Maintenance)) {
    Get-WorkerTaskReconciliation -Desired $Desired -Snapshot $Snapshot -Receipt $Receipt `
        -Maintenance $Maintenance -ExpectedMaintenance $maintenanceExpected -Now $now
}
$d = New-Receipt
$s = New-Snapshot
Check ((Plan $d $s).outcome -eq 'noop') 'owned unchanged is no-op'
$s.tasks[0].state = 'running'
$s.tasks[0].childState = 'unknown'
Check ((Plan $d $s).outcome -eq 'noop') 'configuration no-op independent of health'
$d = New-Receipt 2
$d.releaseSha256 = 'e' * 64
$s = New-Snapshot
$p = Plan $d $s
Check ($p.outcome -eq 'owned_update_plan' -and -not $p.authorizesMutation) 'stopped owned update plan, no authorization'
Check ($p.steps[0] -eq 'journal_prepared' -and $p.steps[-1] -eq 'explicit_admission_release') 'journal before mutation, release last'
$s.tasks[0].state = 'running'
Check ((Plan $d $s).reasonCode -eq 'tasks_or_children_not_stopped') 'running task blocks'
$s.tasks[0].state = 'stopped'
$s.tasks[0].childState = 'unknown'
Check ((Plan $d $s).reasonCode -eq 'tasks_or_children_not_stopped') 'unknown child blocks'
$s = New-Snapshot
$s.journalPending = $true
Check ((Plan $d $s).reasonCode -eq 'recovery_required') 'interrupted transaction blocks automatic retry'
$s = New-Snapshot
$s.tasks = @($s.tasks[0])
Check ((Plan $d $s).reasonCode -eq 'recovery_required') 'partial task pair blocks'
$s = New-Snapshot
Check ((Plan $d $s $null).reasonCode -eq 'ownership_missing') 'unreceipted tasks not adopted'
$s.tasks = @()
Check ((Plan $d $s $null).reasonCode -eq 'fresh_install_requires_approval') 'fresh install not automatically authorized'
foreach ($key in @('releaseSha256', 'configSha256')) {
    $s = New-Snapshot
    $s[$key] = 'f' * 64
    Check ((Plan $d $s).reasonCode -eq 'ownership_drift') "receipt drift $key"
}
$s = New-Snapshot
$s.tasks[1].definitionSha256 = 'f' * 64
Check ((Plan $d $s).reasonCode -eq 'ownership_drift') 'exact task definition drift'
$s = New-Snapshot
$s.ownerSid = 'S-1-5-21-2000'
Reject { Plan $d $s } 'mismatch'
$s = New-Snapshot
$s.tasks[0].taskPath = '\Other\'
Reject { Plan $d $s } 'task identity'
$s = New-Snapshot
$s.tasks[1] = $s.tasks[0].Clone()
Reject { Plan $d $s } 'task identity'
$s = New-Snapshot
Check ((Plan $d $s (New-Receipt) $null).reasonCode -eq 'maintenance_missing') 'no maintenance evidence blocks'
$m = New-Maintenance
$m.queuesPreserved = $false
Check ((Plan $d $s (New-Receipt) $m).reasonCode -eq 'maintenance_not_drained') 'queue preservation blocks update'
$d.generation = 3
Check ((Plan $d $s).reasonCode -eq 'invalid_next_generation') 'skipped generation refused'
$d = New-Receipt 2
Check ((Plan $d $s).reasonCode -eq 'invalid_next_generation') 'unchanged release cannot bump generation'

$old = New-Receipt
$new = New-Receipt 2
$new.releaseSha256 = 'e' * 64
$new.tasks[0].definitionSha256 = 'f' * 64
function Rollback($Snapshot, $Previous = $old, $Candidate = $new, $Maintenance = (New-Maintenance)) {
    Get-WorkerTaskRollbackPlan -Previous $Previous -Candidate $Candidate -Snapshot $Snapshot `
        -Maintenance $Maintenance -ExpectedMaintenance $maintenanceExpected -Now $now
}
foreach ($kind in @('old', 'new', 'mixed', 'missing')) {
    $s = New-Snapshot
    $s.journalPending = $true
    if ($kind -in @('new', 'mixed')) { $s.tasks[0].definitionSha256 = $new.tasks[0].definitionSha256 }
    if ($kind -eq 'new') { $s.releaseSha256 = $new.releaseSha256 }
    if ($kind -eq 'missing') { $s.tasks = @($s.tasks[1]) }
    $p = Rollback $s
    Check ($p.outcome -eq 'rollback_plan' -and -not $p.authorizesMutation) "known $kind transaction rollback plan"
    Check ($p.steps[-1] -eq 'explicit_admission_release') 'rollback does not implicitly release admission'
}
$s = New-Snapshot
Reject { Rollback $s } 'pending recovery'
$s.journalPending = $true
$s.tasks[1].definitionSha256 = '9' * 64
Check ((Rollback $s).reasonCode -eq 'ownership_drift') 'rollback refuses unknown task instead of overwriting'
$s = New-Snapshot
$s.journalPending = $true
$s.configSha256 = '8' * 64
Check ((Rollback $s).reasonCode -eq 'ownership_drift') 'rollback refuses unknown files'
$s = New-Snapshot
$s.journalPending = $true
$s.tasks[0].childState = 'running'
Check ((Rollback $s).reasonCode -eq 'tasks_or_children_not_stopped') 'rollback cannot cancel running child'
$s.tasks[0].childState = 'stopped'
$m = New-Maintenance
$m.respawnSuppressed = $false
Check ((Rollback $s $old $new $m).reasonCode -eq 'maintenance_not_drained') 'rollback needs respawn suppression'
$wrong = New-Receipt 2
$wrong.installationId = '66666666-6666-4666-8666-666666666666'
Reject { Rollback $s $old $wrong } 'mismatch'
$wrong = New-Receipt 3
Reject { Rollback $s $old $wrong } 'adjacent'
$m = New-Maintenance
$m.expiresAtUtc = '2026-10-22T06:30:00Z'
Reject { Rollback $s $old $new $m } 'expired|lifetime'
Write-Host "$passed lifecycle checks passed."
