#Requires -Version 7.0
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'WorkerTransaction.psm1') -Force
$testRoot = Join-Path $PSScriptRoot ('test-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$passed=0
function Check($Condition,$Name) { if (-not $Condition) { throw "FAIL: $Name" }; $script:passed++ }
function Reject([scriptblock]$Body,[string]$Pattern) {
    $message=''; try { & $Body | Out-Null } catch { $message=$_.Exception.Message }
    Check ($message -match $Pattern) "Expected $Pattern, got $message"
}
function Copy-Fixture($Value) {
    & (Get-Module WorkerTransaction) { param($Json) Read-WorkerJson $Json } ($Value | ConvertTo-Json -Depth 40)
}
function Setup {
    $script:dir=Join-Path $testRoot ([guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($dir) | Out-Null
    $script:old=@{schemaVersion=1; installationId='11111111-1111-4111-8111-111111111111'; host='TEST-WORKER'; profile='pilot-remote'; ownerSid='S-1-5-21-123'; generation=1; releaseSha256=('a'*64); configSha256=('b'*64); tasks=@('Tunnel','Daemon' | ForEach-Object { @{taskPath='\'; taskName="MulticaWorker-pilot-remote-$_"; definitionSha256=('c'*64)} })}
    $script:new=Copy-Fixture $old; $new.generation=2; $new.releaseSha256='d'*64
    foreach ($t in $new.tasks) { $t.definitionSha256='e'*64 }
    $script:binding=@{host=$old.host;profile=$old.profile;holder='executor-1';generation=7}
    $script:f=@{writes=0; reads=0; leaseReads=0; staleAt=0; crash=''; failWrite=0; unknown=$false; snapshot=@{host=$old.host;profile=$old.profile;ownerSid=$old.ownerSid;releaseSha256=$old.releaseSha256;configSha256=$old.configSha256;journalPending=$false;tasks=@($old.tasks | ForEach-Object { $t=Copy-Fixture $_; $t.state='stopped';$t.childState='stopped';$t })}}
    $script:adapters=@{
        Now={ [DateTimeOffset]::Parse('2026-10-22T06:30:00Z') }
        Snapshot={ $script:f.reads++; Copy-Fixture $script:f.snapshot }
        Maintenance={
            $script:f.leaseReads++
            $generation=7
            if ($script:f.staleAt -gt 0 -and $script:f.leaseReads -ge $script:f.staleAt) { $generation=8 }
            @{schemaVersion=1;host='TEST-WORKER';profile='pilot-remote';holder='executor-1';generation=$generation;observedAtUtc='2026-10-22T06:30:00Z';expiresAtUtc='2026-10-22T06:30:40Z';state='drained';newAdmissionsBlocked=$true;inflightAdmissions=0;activeChildren=0;resourceWaitAccounted=$true;queuesPreserved=$true;respawnSuppressed=$true}
        }
        RegisterDisabled={ param($task,$expectedHash,$lease)
            $script:f.writes++
            if ($script:f.writes -eq $script:f.failWrite) { throw 'Injected scheduler failure' }
            $actual=@($script:f.snapshot.tasks | Where-Object taskName -CEQ $task.taskName)
            if ($actual.Count -ne 1 -or $actual[0].definitionSha256 -cne $expectedHash -or $lease.generation -ne 7) { throw 'Fake conditional write conflict' }
            $actual[0].definitionSha256=$task.definitionSha256
        }
        Checkpoint={param($point) if ($point -ceq $script:f.crash) { throw "Injected crash at $point" }}
    }
    New-WorkerSimulationStore -Directory $dir -Receipt $old -ExecutionMode Simulation
}
function Run([string]$Operation='Update') {
    Invoke-WorkerStoppedTransaction -Directory $dir -Desired $new -ExpectedMaintenance $binding -Adapters $adapters -Operation $Operation -ExecutionMode Simulation
}
try {
    $decoded = Copy-Fixture @{at='2026-10-22T06:30:00Z'; empty=@(); count=1; flag=$true}
    Check ($decoded.at -is [string] -and $decoded.at -ceq '2026-10-22T06:30:00Z') 'UTC timestamps remain exact strings on PowerShell 7.0+'
    Check ($decoded.empty -is [array] -and $decoded.empty.Count -eq 0) 'empty task arrays remain arrays'
    Reject { & (Get-Module WorkerTransaction) { Read-WorkerJson '{"duplicate":1,"duplicate":2}' } } 'Duplicate stored JSON'
    Setup
    Reject { Invoke-WorkerStoppedTransaction -Directory $dir -Desired $new -Adapters $adapters } 'Production execution unavailable'
    Check ($f.reads -eq 0 -and $f.writes -eq 0) 'production refusal before callbacks'
    $before=Get-Content (Join-Path $dir 'ownership-state.json') -Raw
    $new=Copy-Fixture $old
    Check ((Run).outcome -eq 'noop') 'exact no-op'
    Check ($before -ceq (Get-Content (Join-Path $dir 'ownership-state.json') -Raw)) 'no-op zero store writes'
    Check ($f.writes -eq 0 -and $f.leaseReads -eq 0) 'no-op zero adapter mutations/lease calls'
    foreach ($kind in @('drift','running','unknown','partial')) {
        Setup
        switch ($kind) {
            drift { $f.snapshot.tasks[0].definitionSha256='f'*64 }
            running { $f.snapshot.tasks[0].childState='running' }
            unknown { $f.snapshot.tasks[0].childState='unknown' }
            partial { $f.snapshot.tasks=@($f.snapshot.tasks[0]) }
        }
        Reject { Run } 'Update refused'
        Check ($f.writes -eq 0 -and $null -eq (Get-WorkerStoredState $dir).journal) "No writes for $kind"
    }
    Setup
    $absent=Join-Path $testRoot 'absent'; [IO.Directory]::CreateDirectory($absent) | Out-Null
    Reject { Invoke-WorkerStoppedTransaction -Directory $absent -Desired $new -Adapters $adapters -ExecutionMode Simulation } 'receipt missing'
    Reject { New-WorkerSimulationStore -Directory $dir -Receipt $old -ExecutionMode Simulation } 'already exists'
    Setup
    Check ((Run).outcome -eq 'staged_stopped') 'staged disabled pair'
    Check ((Get-WorkerStoredState $dir).receipt.generation -eq 1) 'no premature candidate receipt commit'
    Reject { Run } 'recovery_required'
    Check ((Run Rollback).outcome -eq 'rolled_back_stopped') 'explicit rollback'
    Check ((Get-WorkerStoredState $dir).journal.phase -eq 'rolled_back') 'rollback durable terminal phase'
    Setup; $f.failWrite=2
    Reject { Run } 'scheduler failure'
    Check ((Get-WorkerStoredState $dir).journal.phase -eq 'Update-intent-Daemon') 'second write failure retains intent'
    $f.failWrite=0
    Check ((Run Rollback).outcome -eq 'rolled_back_stopped') 'partial second write rollback'
    $points=@('journal:prepared','journal:Update-intent-Tunnel','registered:Update-Tunnel','journal:Update-verified-Tunnel','journal:Update-intent-Daemon','registered:Update-Daemon','journal:Update-verified-Daemon','journal:registered_awaiting_canary')
    foreach ($point in $points) {
        Setup; $f.crash=$point
        Reject { Run } 'Injected crash'
        # Reimport simulates a new coordinator invocation loading only disk state.
        Import-Module (Join-Path $PSScriptRoot 'WorkerTransaction.psm1') -Force
        $f.crash=''
        Check ((Run Rollback).outcome -eq 'rolled_back_stopped') "Recovery from $point"
        Check (@($f.snapshot.tasks | Where-Object definitionSha256 -NE ('c'*64)).Count -eq 0) 'both old definitions restored'
    }
    foreach ($phase in @('prepared','Update-intent-Tunnel','Update-verified-Tunnel','Update-intent-Daemon','Update-verified-Daemon','registered_awaiting_canary')) {
        Setup; $f.crash="storage:${phase}:before_publish"
        Reject { Run } 'Injected crash'
        $disk=Get-WorkerStoredState $dir
        Check ($null -ne $disk) 'previous complete store survives unpublished write'
        $f.crash=''
        if ($phase -eq 'prepared') {
            Check ($null -eq $disk.journal -and $f.writes -eq 0) 'unpublished preparation changes no scheduler state'
        } else { Check ((Run Rollback).outcome -eq 'rolled_back_stopped') "Recover unpublished $phase" }
    }
    foreach ($point in @('journal:rollback_prepared','journal:Rollback-intent-Tunnel','registered:Rollback-Tunnel','journal:Rollback-verified-Tunnel','journal:Rollback-intent-Daemon','registered:Rollback-Daemon','journal:Rollback-verified-Daemon','journal:rolled_back')) {
        Setup; $null=Run; $f.crash=$point
        Reject { Run Rollback } 'Injected crash'
        $f.crash=''
        if ($point -eq 'journal:rolled_back') {
            Reject { Run Rollback } 'No pending'
            Check ((Get-WorkerStoredState $dir).receipt.generation -eq 1) 'terminal rollback survives lost response'
        } else { Check ((Run Rollback).outcome -eq 'rolled_back_stopped') "Rollback resumes at $point" }
    }
    Setup; $f.staleAt=1
    Reject { Run } 'generation mismatch'
    Check ($f.writes -eq 0) 'stale initial generation no scheduler write'
    Setup; $f.staleAt=4
    Reject { Run } 'generation mismatch'
    Check ($f.writes -eq 1) 'generation changes before second write'
    Reject { Run Rollback } 'generation mismatch'
    Setup; $null=Run; $f.failWrite=3
    Reject { Run Rollback } 'scheduler failure'
    Check ((Get-WorkerStoredState $dir).journal.phase -eq 'Rollback-intent-Tunnel') 'rollback failure stays pending'
    $f.failWrite=0; $f.snapshot.tasks[0].definitionSha256='f'*64
    Reject { Run Rollback } 'ownership_drift'
    Setup; $null=Run; $binding.generation=8
    Reject { Run Rollback } 'binding changed'
    Setup; $null=Run; $f.snapshot.tasks[0].childState='running'
    Reject { Run Rollback } 'tasks_or_children_not_stopped'
    Setup; $null=Run
    $f.snapshot.releaseSha256='d'*64
    Reject { Run Rollback } 'Previous immutable file pair not restored'
    Check ((Get-WorkerStoredState $dir).journal.phase -ne 'rolled_back') 'rollback cannot claim restored files'
    Setup
    $statePath=Join-Path $dir 'ownership-state.json'
    [IO.File]::WriteAllText($statePath,'{"schemaVersion":1,"payload":"{}","sha256":"bad"}')
    Reject { Get-WorkerStoredState $dir } 'envelope/digest'
    Write-Output "PASS: $passed transaction/storage checks; fake scheduler only."
} finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $allowed=[IO.Path]::GetFullPath($PSScriptRoot) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($allowed,[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe test cleanup path' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
