#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Record {
    param($Record, [string[]]$Fields, [string]$Label)
    if ($Record -isnot [System.Collections.IDictionary]) { throw "$Label must be an object." }
    foreach ($key in $Record.Keys) {
        if ($key -cnotin $Fields) { throw "Unknown ${Label} field: $key." }
    }
    foreach ($key in $Fields) {
        if (-not $Record.Contains($key) -or $null -eq $Record[$key]) { throw "Missing ${Label}.$key." }
    }
}

function Assert-Text {
    param($Value, [string]$Label)
    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value) -or
        $Value -match '[\r\n]' -or $Value.Length -gt 2048) { throw "Invalid $Label." }
}

function Assert-Integer {
    param($Value, [long]$Minimum, [long]$Maximum, [string]$Label)
    if (($Value -isnot [int] -and $Value -isnot [long]) -or $Value -lt $Minimum -or $Value -gt $Maximum) {
        throw "$Label must be an integer between $Minimum and $Maximum."
    }
}

function Assert-Hash {
    param($Value)
    if ($Value -isnot [string] -or $Value -cnotmatch '^[0-9a-fA-F]{64}$') { throw 'Invalid SHA256.' }
}

function Assert-Identifier {
    param($Value)
    $id = [guid]::Empty
    if ($Value -isnot [string] -or -not [guid]::TryParseExact($Value, 'D', [ref]$id) -or $id -eq [guid]::Empty) {
        throw 'Invalid nonempty UUID.'
    }
}

function Get-UtcTime {
    param($Value)
    if ($Value -isnot [string] -or $Value -cnotmatch '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,7})?Z$') {
        throw 'Expected an explicit UTC timestamp ending in Z.'
    }
    return [DateTimeOffset]::Parse($Value, [Globalization.CultureInfo]::InvariantCulture)
}

function Assert-Binding {
    param($Actual, $Expected)
    foreach ($key in $Expected.Keys) {
        Assert-Text $Expected[$key] "expected.$key"
        if (-not $Actual.Contains($key) -or $Actual[$key] -isnot [string]) { throw "Missing binding: $key." }
        $comparison = if ($key -in @('host', 'ownerSid')) {
            [StringComparison]::OrdinalIgnoreCase
        } else { [StringComparison]::Ordinal }
        if (-not [string]::Equals($Actual[$key], $Expected[$key], $comparison)) { throw "Identity mismatch: $key." }
    }
}

function Test-WorkerReadinessRecord {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Record,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Expected,
        [Parameter(Mandatory)][int]$ExitCode,
        [DateTimeOffset]$Now = [DateTimeOffset]::UtcNow
    )
    $binding = @('host', 'profile', 'expectedWorkspaceId', 'ownerId', 'endpoint')
    Assert-Record $Expected $binding 'expected'
    Assert-Record $Record (@('schemaVersion', 'observationId', 'observedAtUtc', 'state', 'reasonCode',
        'apiAuthenticated', 'forwardedEndpointVerified', 'durationMs') + $binding) 'readiness'
    Assert-Integer $Record.schemaVersion 1 1 'schemaVersion'
    Assert-Identifier $Record.observationId
    Assert-Identifier $Record.ownerId
    Assert-Identifier $Record.expectedWorkspaceId
    Assert-Binding $Record $Expected
    $endpoint = $null
    if (-not [uri]::TryCreate($Record.endpoint, [UriKind]::Absolute, [ref]$endpoint) -or
        $endpoint.Scheme -cne 'http' -or $endpoint.Host -notin @('localhost', '127.0.0.1') -or
        $endpoint.Port -lt 1 -or $endpoint.Port -gt 65535 -or $endpoint.AbsolutePath -cne '/' -or
        $endpoint.UserInfo -or $endpoint.Query -or $endpoint.Fragment) {
        throw 'Readiness requires the approved loopback HTTP root endpoint.'
    }
    if ([Text.Encoding]::UTF8.GetByteCount(($Record | ConvertTo-Json -Depth 5 -Compress)) -gt 16384) {
        throw 'Readiness record exceeds 16 KiB.'
    }
    Assert-Integer $Record.durationMs 0 10000 'durationMs'
    $age = ($Now - (Get-UtcTime $Record.observedAtUtc)).TotalSeconds
    if ($age -lt 0 -or $age -gt 45) { throw 'Readiness evidence is future-dated or stale.' }
    foreach ($key in @('apiAuthenticated', 'forwardedEndpointVerified')) {
        if ($Record[$key] -isnot [bool]) { throw "$key must be a boolean." }
    }
    $states = @{
        ready = @{ exit = 0; reasons = @('ready') }
        waiting_dependency = @{ exit = 2; reasons = @('dependency_unavailable') }
        reauth_required = @{ exit = 3; reasons = @('authentication_expired') }
        failed = @{ exit = 1; reasons = @('access_denied', 'identity_mismatch', 'endpoint_mismatch', 'protocol_error', 'timeout') }
    }
    Assert-Text $Record.state 'state'
    if ($Record.state -cnotin @($states.Keys) -or $ExitCode -ne $states[$Record.state].exit -or
        $Record.reasonCode -cnotin $states[$Record.state].reasons) { throw 'Readiness state/reason/exit mismatch.' }
    $ready = $Record.state -ceq 'ready'
    if ($ready -and (-not $Record.apiAuthenticated -or -not $Record.forwardedEndpointVerified)) {
        throw 'Ready requires authenticated API and independently verified forwarding.'
    }
    [ordered]@{
        state = $Record.state
        reasonCode = $Record.reasonCode
        observationId = $Record.observationId
        dependencyReady = $ready
        authorizesAdmission = $false
    }
}

function Test-WorkerMaintenanceDecision {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Decision,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Expected,
        [DateTimeOffset]$Now = [DateTimeOffset]::UtcNow
    )
    Assert-Record $Expected @('host', 'profile', 'holder', 'generation') 'expectedMaintenance'
    Assert-Record $Decision @('schemaVersion', 'host', 'profile', 'holder', 'generation',
        'observedAtUtc', 'expiresAtUtc', 'state', 'newAdmissionsBlocked', 'inflightAdmissions',
        'activeChildren', 'resourceWaitAccounted', 'queuesPreserved', 'respawnSuppressed') 'maintenance'
    Assert-Integer $Decision.schemaVersion 1 1 'schemaVersion'
    foreach ($key in @('generation', 'inflightAdmissions', 'activeChildren')) {
        Assert-Integer $Decision[$key] 0 ([long]::MaxValue) $key
    }
    Assert-Integer $Expected.generation 1 ([long]::MaxValue) 'expected.generation'
    Assert-Binding $Decision @{ host = $Expected.host; profile = $Expected.profile; holder = $Expected.holder }
    if ($Decision.generation -ne $Expected.generation) { throw 'Maintenance generation mismatch.' }
    $observed = Get-UtcTime $Decision.observedAtUtc
    $expires = Get-UtcTime $Decision.expiresAtUtc
    if ($observed -gt $Now -or ($Now - $observed).TotalSeconds -gt 45 -or
        $expires -le $Now -or $expires -le $observed -or ($expires - $observed).TotalSeconds -gt 45) {
        throw 'Maintenance decision is expired, stale or has an invalid lifetime.'
    }
    foreach ($key in @('newAdmissionsBlocked', 'resourceWaitAccounted', 'queuesPreserved', 'respawnSuppressed')) {
        if ($Decision[$key] -isnot [bool]) { throw "$key must be a boolean." }
    }
    if ($Decision.state -cnotin @('open', 'held', 'draining', 'drained')) { throw 'Unknown maintenance state.' }
    $problems = [Collections.Generic.List[string]]::new()
    if ($Decision.state -cne 'drained') { $problems.Add('state_not_drained') }
    foreach ($key in @('newAdmissionsBlocked', 'resourceWaitAccounted', 'queuesPreserved', 'respawnSuppressed')) {
        if (-not $Decision[$key]) { $problems.Add($key) }
    }
    if ($Decision.inflightAdmissions -ne 0) { $problems.Add('inflightAdmissions') }
    if ($Decision.activeChildren -ne 0) { $problems.Add('activeChildren') }
    [ordered]@{ drained = $problems.Count -eq 0; blockers = @($problems.ToArray()); generation = $Decision.generation }
}

function Assert-TaskSet {
    param($Tasks, [string]$Profile, [switch]$Observed, [switch]$AllowMissing)
    if ($Tasks -isnot [array] -or $Tasks.Count -gt 2 -or (-not $AllowMissing -and $Tasks.Count -ne 2)) {
        throw 'Task set must contain exactly the two owned component identities.'
    }
    $seen = @{}
    foreach ($task in $Tasks) {
        $fields = @('taskPath', 'taskName', 'definitionSha256')
        if ($Observed) { $fields += @('state', 'childState') }
        Assert-Record $task $fields 'task'
        if ($task.taskPath -cne '\' -or $task.taskName -cnotin @("MulticaWorker-$Profile-Tunnel", "MulticaWorker-$Profile-Daemon") -or
            $seen.ContainsKey($task.taskName)) { throw 'Unexpected or duplicate task identity.' }
        $seen[$task.taskName] = $true
        Assert-Hash $task.definitionSha256
        if ($Observed -and ($task.state -cnotin @('stopped', 'running', 'unknown') -or
            $task.childState -cnotin @('stopped', 'running', 'unknown'))) { throw 'Invalid task/child state.' }
    }
}

function Assert-Receipt {
    param($Receipt)
    Assert-Record $Receipt @('schemaVersion', 'installationId', 'host', 'profile', 'ownerSid',
        'generation', 'releaseSha256', 'configSha256', 'tasks') 'receipt'
    Assert-Integer $Receipt.schemaVersion 1 1 'schemaVersion'
    Assert-Identifier $Receipt.installationId
    Assert-Integer $Receipt.generation 1 ([long]::MaxValue) 'receipt.generation'
    foreach ($key in @('host', 'profile', 'ownerSid')) { Assert-Text $Receipt[$key] $key }
    if ($Receipt.profile -cnotmatch '^[A-Za-z0-9_-]+$' -or $Receipt.ownerSid -cnotmatch '^S-1-\d+(?:-\d+)+$') {
        throw 'Invalid profile or owner SID.'
    }
    Assert-Hash $Receipt.releaseSha256
    Assert-Hash $Receipt.configSha256
    Assert-TaskSet $Receipt.tasks $Receipt.profile
}

function Get-WorkerTaskReconciliation {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Desired,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Snapshot,
        [AllowNull()][System.Collections.IDictionary]$Receipt,
        [AllowNull()][System.Collections.IDictionary]$Maintenance,
        [AllowNull()][System.Collections.IDictionary]$ExpectedMaintenance,
        [DateTimeOffset]$Now = [DateTimeOffset]::UtcNow
    )
    Assert-Receipt $Desired
    Assert-Record $Snapshot @('host', 'profile', 'ownerSid', 'releaseSha256', 'configSha256',
        'tasks', 'journalPending') 'snapshot'
    Assert-Binding $Snapshot @{ host = $Desired.host; profile = $Desired.profile; ownerSid = $Desired.ownerSid }
    if ($Snapshot.journalPending -isnot [bool]) { throw 'journalPending must be a boolean.' }
    Assert-TaskSet $Snapshot.tasks $Desired.profile -Observed -AllowMissing
    $outcome = 'blocked'
    $reason = 'ownership_missing'
    $steps = @()
    if ($Snapshot.journalPending -or $Snapshot.tasks.Count -eq 1) {
        $reason = 'recovery_required'
    } elseif ($null -eq $Receipt) {
        if ($Snapshot.tasks.Count -eq 0) { $reason = 'fresh_install_requires_approval' }
    } else {
        Assert-Receipt $Receipt
        Assert-Binding $Receipt @{ host = $Desired.host; profile = $Desired.profile; ownerSid = $Desired.ownerSid;
            installationId = $Desired.installationId }
        Assert-Hash $Snapshot.releaseSha256
        Assert-Hash $Snapshot.configSha256
        $drift = $Snapshot.tasks.Count -ne 2 -or $Snapshot.releaseSha256 -ine $Receipt.releaseSha256 -or
            $Snapshot.configSha256 -ine $Receipt.configSha256
        $same = $Desired.releaseSha256 -ieq $Receipt.releaseSha256 -and $Desired.configSha256 -ieq $Receipt.configSha256
        foreach ($old in $Receipt.tasks) {
            $actual = @($Snapshot.tasks | Where-Object taskName -CEQ $old.taskName)
            $new = @($Desired.tasks | Where-Object taskName -CEQ $old.taskName)
            if ($actual.Count -ne 1 -or $actual[0].definitionSha256 -ine $old.definitionSha256) { $drift = $true }
            if ($new[0].definitionSha256 -ine $old.definitionSha256) { $same = $false }
        }
        if ($drift) {
            $reason = 'ownership_drift'
        } elseif ($same -and $Desired.generation -eq $Receipt.generation) {
            $outcome = 'noop'
            $reason = 'exact_owned_match'
        } elseif ($same -or $Desired.generation -ne ($Receipt.generation + 1)) {
            $reason = 'invalid_next_generation'
        } elseif (@($Snapshot.tasks | Where-Object { $_.state -cne 'stopped' -or $_.childState -cne 'stopped' }).Count) {
            $reason = 'tasks_or_children_not_stopped'
        } elseif ($null -eq $Maintenance -or $null -eq $ExpectedMaintenance) {
            $reason = 'maintenance_missing'
        } else {
            if ($ExpectedMaintenance.host -ine $Desired.host -or $ExpectedMaintenance.profile -cne $Desired.profile) {
                throw 'Maintenance is bound to a different target.'
            }
            $gate = Test-WorkerMaintenanceDecision $Maintenance $ExpectedMaintenance $Now
            if (-not $gate.drained) {
                $reason = 'maintenance_not_drained'
            } else {
                $outcome = 'owned_update_plan'
                $reason = 'review_required'
                $steps = @('journal_prepared', 'revalidate_ownership_and_lease', 'register_tunnel_disabled',
                    'verify_tunnel', 'revalidate_ownership_and_lease', 'register_daemon_disabled', 'verify_daemon',
                    'journal_registered', 'approved_canary_cutover', 'verify_readiness_and_provider_canary',
                    'commit_receipt_atomically', 'journal_verified', 'explicit_admission_release')
            }
        }
    }
    [ordered]@{ outcome = $outcome; reasonCode = $reason; steps = $steps; authorizesMutation = $false }
}

function Get-WorkerTaskRollbackPlan {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Previous,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Candidate,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Snapshot,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Maintenance,
        [Parameter(Mandatory)][System.Collections.IDictionary]$ExpectedMaintenance,
        [DateTimeOffset]$Now = [DateTimeOffset]::UtcNow
    )
    Assert-Receipt $Previous
    Assert-Receipt $Candidate
    Assert-Binding $Candidate @{ host = $Previous.host; profile = $Previous.profile; ownerSid = $Previous.ownerSid;
        installationId = $Previous.installationId }
    if ($Candidate.generation -ne ($Previous.generation + 1)) { throw 'Rollback requires adjacent receipt generations.' }
    Assert-Record $Snapshot @('host', 'profile', 'ownerSid', 'releaseSha256', 'configSha256',
        'tasks', 'journalPending') 'snapshot'
    Assert-Binding $Snapshot @{ host = $Previous.host; profile = $Previous.profile; ownerSid = $Previous.ownerSid }
    Assert-TaskSet $Snapshot.tasks $Previous.profile -Observed -AllowMissing
    Assert-Hash $Snapshot.releaseSha256
    Assert-Hash $Snapshot.configSha256
    if ($Snapshot.journalPending -isnot [bool] -or -not $Snapshot.journalPending) {
        throw 'Rollback planning requires an explicitly pending recovery transaction.'
    }
    if ($ExpectedMaintenance.host -ine $Previous.host -or $ExpectedMaintenance.profile -cne $Previous.profile) {
        throw 'Maintenance is bound to a different rollback target.'
    }
    $gate = Test-WorkerMaintenanceDecision $Maintenance $ExpectedMaintenance $Now
    $reason = 'review_required'
    $steps = @()
    $knownFiles = ($Snapshot.releaseSha256 -ieq $Previous.releaseSha256 -and $Snapshot.configSha256 -ieq $Previous.configSha256) -or
        ($Snapshot.releaseSha256 -ieq $Candidate.releaseSha256 -and $Snapshot.configSha256 -ieq $Candidate.configSha256)
    if (-not $knownFiles) {
        $reason = 'ownership_drift'
    } elseif (-not $gate.drained) {
        $reason = 'maintenance_not_drained'
    } elseif (@($Snapshot.tasks | Where-Object { $_.state -cne 'stopped' -or $_.childState -cne 'stopped' }).Count) {
        $reason = 'tasks_or_children_not_stopped'
    } else {
        foreach ($task in $Snapshot.tasks) {
            $old = @($Previous.tasks | Where-Object taskName -CEQ $task.taskName)[0]
            $new = @($Candidate.tasks | Where-Object taskName -CEQ $task.taskName)[0]
            if ($task.definitionSha256 -ine $old.definitionSha256 -and $task.definitionSha256 -ine $new.definitionSha256) {
                $reason = 'ownership_drift'
            }
        }
        if ($reason -eq 'review_required') {
            $steps = @('journal_rollback_prepared', 'revalidate_ownership_and_lease',
                'restore_both_previous_task_definitions_disabled', 'verify_previous_definitions',
                'restore_previous_receipt_atomically', 'approved_baseline_start', 'verify_previous_health',
                'journal_rolled_back', 'explicit_admission_release')
        }
    }
    [ordered]@{
        outcome = $(if ($steps.Count) { 'rollback_plan' } else { 'blocked' })
        reasonCode = $reason
        steps = $steps
        authorizesMutation = $false
    }
}

Export-ModuleMember -Function Test-WorkerReadinessRecord, Test-WorkerMaintenanceDecision,
    Get-WorkerTaskReconciliation, Get-WorkerTaskRollbackPlan
