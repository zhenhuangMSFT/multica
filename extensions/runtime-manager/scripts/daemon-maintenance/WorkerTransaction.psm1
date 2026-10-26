#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'WorkerLifecycle.psm1')

function Assert-Simulation([string]$ExecutionMode) {
    if ($ExecutionMode -cne 'Simulation') {
        throw 'Production execution unavailable: enforced lease, trusted receipt provenance and fenced host execution are not implemented.'
    }
}
function Copy-Record($Value) {
    Read-WorkerJson ($Value | ConvertTo-Json -Depth 40 -Compress)
}
function Read-WorkerJson([string]$Text) {
    function Convert-Node([System.Text.Json.JsonElement]$Node) {
        switch ($Node.ValueKind.ToString()) {
            Object {
                $value = [ordered]@{}
                foreach ($property in $Node.EnumerateObject()) {
                    if ($value.Contains($property.Name)) { throw 'Duplicate stored JSON property.' }
                    $value[$property.Name] = Convert-Node $property.Value
                }
                return $value
            }
            Array { return ,@($Node.EnumerateArray() | ForEach-Object { Convert-Node $_ }) }
            String { return $Node.GetString() }
            Number {
                $integer = 0L
                if ($Node.TryGetInt64([ref]$integer)) { return $integer }
                return $Node.GetDouble()
            }
            True { return $true }
            False { return $false }
            Null { return $null }
            default { throw 'Unsupported stored JSON value.' }
        }
    }
    $options = [System.Text.Json.JsonDocumentOptions]::new()
    $options.MaxDepth = 40
    $document = [System.Text.Json.JsonDocument]::Parse($Text, $options)
    try { Convert-Node $document.RootElement } finally { $document.Dispose() }
}
function Get-Digest([string]$Text) {
    [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($Text)))
}
function Get-StateFile([string]$Directory) {
    $root = [IO.Path]::GetFullPath($Directory)
    if (-not [IO.Directory]::Exists($root)) { throw 'Store directory must already exist.' }
    $item = Get-Item -LiteralPath $root
    while ($null -ne $item) {
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse-point store paths are refused.' }
        $item = $item.Parent
    }
    Join-Path $root 'ownership-state.json'
}
function Read-State([string]$Path) {
    if (-not [IO.File]::Exists($Path)) { return $null }
    if ((Get-Item -LiteralPath $Path).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse-point state refused.' }
    if ((Get-Item -LiteralPath $Path).Length -gt 4MB) { throw 'State exceeds 4 MiB.' }
    $envelope = Read-WorkerJson ([IO.File]::ReadAllText($Path))
    if ($envelope.Count -ne 3 -or $envelope.schemaVersion -ne 1 -or
        $envelope.payload -isnot [string] -or (Get-Digest $envelope.payload) -cne $envelope.sha256) { throw 'Invalid state envelope/digest.' }
    $state = Read-WorkerJson $envelope.payload
    if ($state.Count -ne 4 -or $state.revision -isnot [long] -or $state.revision -lt 1 -or
        -not $state.Contains('receipt') -or -not $state.Contains('journal') -or $state.simulationOnly -ne $true) { throw 'Invalid stored state.' }
    $state
}
function Write-State([string]$Path, $State, [long]$ExpectedRevision, [scriptblock]$BeforePublish = {}) {
    # Caller owns the cooperating-store handle; this is not a host maintenance lock.
    $current = Read-State $Path
    $actual = if ($null -eq $current) { 0 } else { $current.revision }
    if ($actual -ne $ExpectedRevision) { throw 'Store revision conflict.' }
    $State.revision = $ExpectedRevision + 1
    $payload = $State | ConvertTo-Json -Depth 40 -Compress
    $bytes = [Text.Encoding]::UTF8.GetBytes((@{ schemaVersion=1; payload=$payload; sha256=(Get-Digest $payload) } | ConvertTo-Json -Compress))
    if ($bytes.Length -gt 4MB) { throw 'State exceeds 4 MiB.' }
    $temp = "$Path.$([guid]::NewGuid().ToString('N')).tmp"
    try {
        $file = [IO.FileStream]::new($temp, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None, 4096, [IO.FileOptions]::WriteThrough)
        try { $file.Write($bytes,0,$bytes.Length); $file.Flush($true) } finally { $file.Dispose() }
        & $BeforePublish
        if ([IO.File]::Exists($Path)) { [IO.File]::Replace($temp,$Path,[NullString]::Value) }
        else { [IO.File]::Move($temp,$Path) }
    } finally { if ([IO.File]::Exists($temp)) { [IO.File]::Delete($temp) } }
}
function Open-StoreLock([string]$Path) {
    $lockPath = "$Path.lock"
    if ((Test-Path -LiteralPath $lockPath) -and ((Get-Item -LiteralPath $lockPath).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Reparse-point lock refused.' }
    [IO.FileStream]::new($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
}
function New-WorkerSimulationStore {
    [CmdletBinding()]
    param([string]$Directory, [System.Collections.IDictionary]$Receipt, [string]$ExecutionMode='Production')
    Assert-Simulation $ExecutionMode
    # Reuse lifecycle schema/identity validation with an exact owned snapshot.
    $snapshot = @{ host=$Receipt.host; profile=$Receipt.profile; ownerSid=$Receipt.ownerSid; releaseSha256=$Receipt.releaseSha256; configSha256=$Receipt.configSha256; journalPending=$false; tasks=@($Receipt.tasks | ForEach-Object { @{taskPath=$_.taskPath; taskName=$_.taskName; definitionSha256=$_.definitionSha256; state='stopped'; childState='stopped'} }) }
    $null = Get-WorkerTaskReconciliation -Desired $Receipt -Receipt $Receipt -Snapshot $snapshot
    $path = Get-StateFile $Directory
    $lock = Open-StoreLock $path
    try {
        if ($null -ne (Read-State $path)) { throw 'Store already exists; refusing adoption.' }
        Write-State $path @{ revision=0; receipt=(Copy-Record $Receipt); journal=$null; simulationOnly=$true } 0
    } finally { $lock.Dispose() }
}
function Get-WorkerStoredState {
    param([string]$Directory)
    Read-State (Get-StateFile $Directory)
}
function Invoke-WorkerStoppedTransaction {
    [CmdletBinding()]
    param(
        [string]$Directory,
        [ValidateSet('Update','Rollback')][string]$Operation='Update',
        [System.Collections.IDictionary]$Desired,
        [System.Collections.IDictionary]$ExpectedMaintenance,
        [System.Collections.IDictionary]$Adapters,
        [string]$ExecutionMode='Production'
    )
    Assert-Simulation $ExecutionMode
    foreach ($key in @('Snapshot','Maintenance','RegisterDisabled','Now','Checkpoint')) {
        if ($Adapters[$key] -isnot [scriptblock]) { throw "Missing simulation adapter: $key" }
    }
    $path = Get-StateFile $Directory
    $state = Read-State $path
    if ($null -eq $state) { throw 'Trusted receipt missing; fresh install/adoption unavailable.' }
    $pending = $null -ne $state.journal -and $state.journal.phase -cne 'rolled_back'
    $snapshot = & $Adapters.Snapshot
    # Snapshot adapter reports tasks/files; durable journal owns pending status.
    $snapshot.journalPending = $pending
    if ($Operation -eq 'Update') {
        $plan = Get-WorkerTaskReconciliation -Desired $Desired -Receipt $state.receipt -Snapshot $snapshot
        if ($plan.outcome -eq 'noop') { return @{outcome='noop'; productionEnabled=$false} }
        if ($plan.reasonCode -cne 'maintenance_missing') { throw "Update refused: $($plan.reasonCode)" }
    } elseif (-not $pending) { throw 'No pending transaction to roll back.' }
    $lock = Open-StoreLock $path
    try {
        $latest = Read-State $path
        if ($latest.revision -ne $state.revision) { throw 'Store revision conflict.' }
        function Save-Phase([string]$Phase) {
            $state.journal.phase = $Phase
            $state.journal.events += @{phase=$Phase; atUtc=((& $Adapters.Now).UtcDateTime.ToString('o'))}
            Write-State $path $state $state.revision { & $Adapters.Checkpoint "storage:${Phase}:before_publish" }
            & $Adapters.Checkpoint "journal:$Phase"
        }
        function Observe-Gated {
            $decision = & $Adapters.Maintenance
            $observation = & $Adapters.Snapshot
            $observation.journalPending = $true
            $rollback = Get-WorkerTaskRollbackPlan -Previous $state.journal.previous -Candidate $state.journal.candidate -Snapshot $observation -Maintenance $decision -ExpectedMaintenance $ExpectedMaintenance -Now (& $Adapters.Now)
            if ($rollback.outcome -cne 'rollback_plan') { throw "Transaction refused: $($rollback.reasonCode)" }
            $observation
        }
        if ($Operation -eq 'Update') {
            $decision = & $Adapters.Maintenance
            $snapshot = & $Adapters.Snapshot
            $snapshot.journalPending = $false
            $plan = Get-WorkerTaskReconciliation -Desired $Desired -Receipt $state.receipt -Snapshot $snapshot -Maintenance $decision -ExpectedMaintenance $ExpectedMaintenance -Now (& $Adapters.Now)
            if ($plan.outcome -cne 'owned_update_plan') { throw "Update refused: $($plan.reasonCode)" }
            $state.journal = @{transactionId=[guid]::NewGuid().ToString(); previous=(Copy-Record $state.receipt); candidate=(Copy-Record $Desired); expectedMaintenance=(Copy-Record $ExpectedMaintenance); phase='prepared'; events=@()}
            Save-Phase 'prepared'
        } else {
            # Recovery must retain the original holder/generation; reassignment is not inferred.
            foreach ($key in @('host','profile','holder','generation')) {
                if ($state.journal.expectedMaintenance[$key] -cne $ExpectedMaintenance[$key]) { throw 'Recovery maintenance binding changed.' }
            }
            $null = Observe-Gated
            Save-Phase 'rollback_prepared'
        }
        $target = if ($Operation -eq 'Update') { $state.journal.candidate } else { $state.journal.previous }
        # Definition hashes here MUST describe disabled definitions. Adapter owns
        # trusted immutable definition material and conditional fenced replacement.
        foreach ($component in @('Tunnel','Daemon')) {
            $task = @($target.tasks | Where-Object { $_.taskName -ceq "MulticaWorker-$($target.profile)-$component" })[0]
            Save-Phase "$Operation-intent-$component"
            $before = Observe-Gated
            $actual = @($before.tasks | Where-Object taskName -CEQ $task.taskName)
            $expectedHash = if ($actual.Count) { $actual[0].definitionSha256 } else { $null }
            & $Adapters.RegisterDisabled (Copy-Record $task) $expectedHash (Copy-Record $ExpectedMaintenance)
            & $Adapters.Checkpoint "registered:$Operation-$component"
            $after = Observe-Gated
            $observedTask = @($after.tasks | Where-Object taskName -CEQ $task.taskName)
            if ($observedTask.Count -ne 1 -or $observedTask[0].definitionSha256 -ine $task.definitionSha256) { throw 'Registration verification failed.' }
            Save-Phase "$Operation-verified-$component"
        }
        $final = Observe-Gated
        foreach ($task in $target.tasks) {
            $actual = @($final.tasks | Where-Object taskName -CEQ $task.taskName)
            if ($actual.Count -ne 1 -or $actual[0].definitionSha256 -ine $task.definitionSha256) { throw 'Final pair verification failed.' }
        }
        if ($Operation -eq 'Rollback') {
            if ($final.releaseSha256 -ine $target.releaseSha256 -or $final.configSha256 -ine $target.configSha256) { throw 'Previous immutable file pair not restored; retain pending journal.' }
            $state.receipt = Copy-Record $state.journal.previous
            Save-Phase 'rolled_back'
            return @{outcome='rolled_back_stopped'; productionEnabled=$false; admissionsReleased=$false}
        }
        Save-Phase 'registered_awaiting_canary'
        # Receipt remains previous until an independently approved cutover/canary.
        @{outcome='staged_stopped'; productionEnabled=$false; admissionsReleased=$false}
    } finally { $lock.Dispose() }
}
Export-ModuleMember -Function New-WorkerSimulationStore, Get-WorkerStoredState, Invoke-WorkerStoppedTransaction
