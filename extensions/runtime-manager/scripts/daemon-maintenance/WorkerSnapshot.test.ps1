#Requires -Version 7.0
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'WorkerSnapshot.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'WorkerLifecycle.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'WorkerTransaction.psm1') -Force
$module = Get-Module WorkerSnapshot
$script:checks = 0
function Check($Condition, [string]$Label) {
    if (-not $Condition) { throw "FAIL: $Label" }
    $script:checks++
}
function Reject([scriptblock]$Action, [string]$Pattern) {
    $message = ''
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Check ($message -like $Pattern) "expected $Pattern; got $message"
}
function Definition([string]$Name = 'MulticaWorker-test-Daemon') {
    @"
<Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task" version="1.4">
  <RegistrationInfo><URI>\$Name</URI><Description>fixture</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>S-1-5-21-123</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Owner"><UserId>S-1-5-21-123</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><Enabled>false</Enabled><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy></Settings>
  <Actions Context="Owner"><Exec><Command>C:\Fixtures\pwsh.exe</Command><Arguments>-File "C:\Fixtures\runner.ps1" --profile test</Arguments><WorkingDirectory>C:\Fixtures</WorkingDirectory></Exec></Actions>
</Task>
"@
}
$xml = Definition
$definition = Get-WorkerDefinition $xml 'MulticaWorker-test-Daemon' 'S-1-5-21-123'
Check ($definition.originalXml -ceq $xml -and -not $definition.enabled) 'exact original XML retained, including enabled state'
Check ($definition.definitionSha256 -cmatch '^[A-F0-9]{64}$') 'deterministic SHA256 format'
Check ($definition.definitionSha256 -ceq '45DB9FC2E22594534B87BBCA7908564661307A44CE1D7106D7C300F1CDB14970') 'versioned canonicalization golden vector'
$culture = [Threading.Thread]::CurrentThread.CurrentCulture
try {
    [Threading.Thread]::CurrentThread.CurrentCulture = [Globalization.CultureInfo]::GetCultureInfo('ar-SA')
    Check ((Get-WorkerDefinition $xml 'MulticaWorker-test-Daemon' 'S-1-5-21-123').definitionSha256 -ceq $definition.definitionSha256) 'definition hashes are culture invariant'
} finally { [Threading.Thread]::CurrentThread.CurrentCulture = $culture }
foreach ($variant in @(
    $xml.Replace("`r`n", "`n"),
    ($xml -replace '>\s+<', '><'),
    $xml.Replace('xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task" version="1.4"', 'version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"'),
    $xml.Replace('<Description>fixture</Description>', '<!--ignored--><Description><![CDATA[fixture]]></Description>'),
    ('<?xml version="1.0" encoding="utf-16"?>' + $xml)
)) {
    Check ((Get-WorkerDefinition $variant 'MulticaWorker-test-Daemon' 'S-1-5-21-123').definitionSha256 -ceq $definition.definitionSha256) 'format/attribute/comment/CDATA/declaration stability'
}
[xml]$prefixed = $xml
foreach ($element in $prefixed.SelectNodes('//*')) { $element.Prefix = 't' }
$null = $prefixed.DocumentElement.SetAttribute('xmlns:t', 'http://schemas.microsoft.com/windows/2004/02/mit/task')
Check ((Get-WorkerDefinition $prefixed.OuterXml 'MulticaWorker-test-Daemon' 'S-1-5-21-123').definitionSha256 -ceq $definition.definitionSha256) 'namespace prefixes do not alter definition hash'
Reject { Get-WorkerDefinition $xml '\MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*Invalid exact task*'
foreach ($change in @(
    @('<Settings><Enabled>false', '<Settings><Enabled>true'),
    @('fixture', 'changed'),
    @('InteractiveToken', 'Password'),
    @('LeastPrivilege', 'HighestAvailable'),
    @('--profile test', '--profile other'),
    @('C:\Fixtures\pwsh.exe', 'C:\Other\pwsh.exe'),
    @('<WorkingDirectory>C:\Fixtures', '<WorkingDirectory>C:\Other'),
    @('IgnoreNew', 'Parallel'),
    @('<LogonTrigger>', '<LogonTrigger id="different">'),
    @('<Arguments>-File', '<Arguments> -File')
)) {
    $changed = Get-WorkerDefinition ($xml.Replace($change[0], $change[1])) 'MulticaWorker-test-Daemon' 'S-1-5-21-123'
    Check ($changed.definitionSha256 -cne $definition.definitionSha256) "definition drift: $($change[0])"
}
Reject { Get-WorkerDefinition $xml 'MulticaWorker-other-Daemon' 'S-1-5-21-123' } '*URI*'
Reject { Get-WorkerDefinition $xml 'MulticaWorker-test-Daemon' 'S-1-5-21-999' } '*principal SID*'
Reject { Get-WorkerDefinition ($xml.Replace('<Settings><Enabled>false', '<Settings><Enabled>0')) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*explicit true/false*'
Reject { Get-WorkerDefinition ($xml.Replace('<Enabled>false</Enabled>', '')) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*exactly one*'
Reject { Get-WorkerDefinition ($xml.Replace('<Enabled>false</Enabled>', '<Enabled>false</Enabled><Enabled>false</Enabled>')) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*exactly one*'
Reject { Get-WorkerDefinition ('<!DOCTYPE Task [<!ENTITY x SYSTEM "file:///not-accessed">]>' + $xml) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*DTD*'
Reject { Get-WorkerDefinition ('<?unsafe instruction?>' + $xml) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*Processing instructions*'
Reject { Get-WorkerDefinition ($xml.Replace('<Actions Context="Owner">', '<Actions Context="Owner">mixed')) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*Mixed*'
Reject { Get-WorkerDefinition ($xml.Replace('</Settings>', '<Unknown>' * 66 + '</Unknown>' * 66 + '</Settings>')) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*depth*'
Reject { Get-WorkerDefinition ('x' * (1MB + 1)) 'MulticaWorker-test-Daemon' 'S-1-5-21-123' } '*1 MiB*'

$script:binding = @{ host = 'FIXTURE-HOST'; profile = 'test'; ownerSid = 'S-1-5-21-123'
    releasePath = 'C:\Fixtures\multica.exe'; configPath = 'C:\Fixtures\worker.json'
    releaseSha256 = 'A' * 64; configSha256 = 'B' * 64 }
function Setup {
    $script:fixture = @{ taskReads = 0; change = $false; taskError = $false; fileError = ''; processError = $false
        fileDrift = $false; identity = @{ host = 'fixture-host'; ownerSid = 'S-1-5-21-123' }
        processes = @(); tasks = @('Tunnel', 'Daemon' | ForEach-Object {
            @{ taskPath = '\'; taskName = "MulticaWorker-test-$_"; xml = (Definition "MulticaWorker-test-$_"); enabled = $false; state = 1 }
        }) }
    $script:readers = @{
        Identity = { $script:fixture.identity }
        File = {
            param($path)
            if ($script:fixture.fileError -ceq $path) { throw 'Fixture file access denied.' }
            $hash = if ($path -ceq $script:binding.configPath) { 'B' * 64 } else { 'A' * 64 }
            if ($script:fixture.fileDrift -and $path -ceq $script:binding.releasePath) { $hash = 'C' * 64 }
            @{ path = $path; sha256 = $hash }
        }
        Tasks = {
            $script:fixture.taskReads++
            if ($script:fixture.taskError) { throw 'Fixture scheduler access denied.' }
            if ($script:fixture.change -and $script:fixture.taskReads -gt 1) { return @() }
            $script:fixture.tasks
        }
        Processes = { if ($script:fixture.processError) { throw 'Fixture process access denied.' }; $script:fixture.processes }
    }
}
function Observe([object[]]$References = @()) {
    & $module { param($b, $r, $a) Read-WorkerSnapshotCore $b $r $a } $script:binding $References $script:readers
}
Setup
$observation = Observe
Check ($observation.snapshot.tasks.Count -eq 2 -and $fixture.taskReads -eq 2) 'pair observed twice'
Check ($observation.filesMatchExpected -and -not $observation.productionEnabled -and -not $observation.authorizesMutation) 'file verification is never production authority'
Check (@($observation.snapshot.tasks | Where-Object { $_.state -ne 'stopped' -or $_.childState -ne 'unknown' }).Count -eq 0) 'disabled scheduler state does not claim child absence'
Check ($observation.childCoverage -eq 'unproven' -and $observation.definitions.Count -eq 2) 'original definitions kept separate from lifecycle schema'
$receipt = @{ schemaVersion = 1; installationId = '11111111-1111-4111-8111-111111111111'
    host = $binding.host; profile = $binding.profile; ownerSid = $binding.ownerSid; generation = 1
    releaseSha256 = $binding.releaseSha256; configSha256 = $binding.configSha256
    tasks = @($observation.snapshot.tasks | ForEach-Object { @{ taskPath = $_.taskPath; taskName = $_.taskName; definitionSha256 = $_.definitionSha256 } }) }
$next = $receipt.Clone(); $next.generation = 2; $next.releaseSha256 = 'D' * 64
$plan = Get-WorkerTaskReconciliation -Desired $next -Receipt $receipt -Snapshot $observation.snapshot
Check ($plan.reasonCode -eq 'tasks_or_children_not_stopped') 'real snapshot schema consumed by shared lifecycle and unknown children block update'
Setup
$fixture.tasks = @($fixture.tasks[0])
Check ((Observe).snapshot.tasks.Count -eq 1) 'partial task pair retained, not synthesized'
Setup
$fixture.tasks = @()
Check ((Observe).snapshot.tasks.Count -eq 0) 'successful empty inventory means missing'
Setup
$fixture.tasks += @{ taskPath = '\Other\'; taskName = 'MulticaWorker-test-Daemon'; xml = 'invalid' }
Check ((Observe).snapshot.tasks.Count -eq 2) 'same name in another folder is never adopted'
Setup
$fixture.taskError = $true
Reject { Observe } '*scheduler access denied*'
Setup
$fixture.processError = $true
Reject { Observe } '*process access denied*'
foreach ($path in @($binding.releasePath, $binding.configPath)) {
    Setup; $fixture.fileError = $path
    Reject { Observe } '*file access denied*'
}
Setup
$fixture.change = $true
Reject { Observe } '*changed during observation*'
Setup
$fixture.tasks[0].enabled = $true
Reject { Observe } '*enabled state disagrees*'
Setup
$fixture.identity.host = 'OTHER'
Reject { Observe } '*host/owner binding*'
Setup
$fixture.identity.ownerSid = 'S-1-5-21-999'
Reject { Observe } '*host/owner binding*'
Setup
$fixture.fileDrift = $true
$drift = Observe
Check (-not $drift.filesMatchExpected -and $drift.snapshot.releaseSha256 -eq ('C' * 64)) 'actual immutable hashes expose drift'
Check ((Get-WorkerTaskReconciliation -Desired $next -Receipt $receipt -Snapshot $drift.snapshot).reasonCode -eq 'ownership_drift') 'file drift reaches lifecycle policy'
foreach ($state in @(0, 2, 4)) {
    Setup; $fixture.tasks[0].state = $state
    $expected = if ($state -eq 4) { 'running' } else { 'unknown' }
    Check ((Observe).snapshot.tasks[0].state -eq $expected) 'queued/unknown/running scheduler states preserved conservatively'
}
$reference = @{ taskName = 'MulticaWorker-test-Daemon'; pid = 123; creationTimeUtc = '2026-10-22T07:00:00Z'
    imagePath = $binding.releasePath; imageSha256 = $binding.releaseSha256 }
foreach ($kind in @('match', 'missing', 'reused', 'image', 'owner', 'unavailable', 'hash')) {
    Setup
    $process = @{ pid = 123; creationTimeUtc = '2026-10-22T07:00:00.0000000Z'; imagePath = $binding.releasePath; ownerSid = $binding.ownerSid }
    switch ($kind) {
        reused { $process.creationTimeUtc = '2026-10-22T07:01:00Z' }
        image { $process.imagePath = 'C:\Other\multica.exe' }
        owner { $process.ownerSid = 'S-1-5-21-999' }
        unavailable { $process.imagePath = $null }
        hash { $fixture.fileDrift = $true }
    }
    if ($kind -ne 'missing') { $fixture.processes = @($process) }
    $result = Observe @($reference)
    $expected = if ($kind -eq 'match') { 'running' } else { 'unknown' }
    Check ($result.processes[0].state -eq $expected -and $result.snapshot.tasks[1].childState -eq $expected) "PID/time/image/owner classification: $kind"
    if ($kind -eq 'match') {
        Check ($result.processes[0].observedIdentity.imageSha256 -ceq $binding.releaseSha256) 'observed image hash retained without command-line content'
    }
    if ($kind -eq 'reused') { Check ($result.processes[0].reasonCode -eq 'pid_reused') 'PID reuse not inferred as old process or safe absence' }
}
Setup
$bad = $reference.Clone(); $bad.taskName = 'MulticaWorker-other-Daemon'
Reject { Observe @($bad) } '*different profile*'
Setup
Reject { Observe @($reference, $reference) } '*Duplicate process reference*'
Setup
$bad = $reference.Clone(); $bad.pid = 0
Reject { Observe @($bad) } '*Invalid process PID*'
Setup
$fixture.tasks = @()
$fixture.processes = @(@{ pid = 123; creationTimeUtc = $reference.creationTimeUtc; imagePath = $reference.imagePath; ownerSid = $binding.ownerSid })
$orphan = Observe @($reference)
Check ($orphan.snapshot.tasks.Count -eq 0 -and $orphan.processes[0].state -eq 'running') 'known orphan retained even when scheduler tasks are missing'
Setup
$imageReference = $reference.Clone(); $imageReference.imagePath = 'C:\Fixtures\child.exe'
$fixture.processes = @(@{ pid = 123; creationTimeUtc = $reference.creationTimeUtc; imagePath = $imageReference.imagePath; ownerSid = $binding.ownerSid })
$fixture.fileError = $imageReference.imagePath
Reject { Observe @($imageReference) } '*file access denied*'
foreach ($hresult in @(-2147024891, -2147024893, -2147216617)) {
    Reject { & $module { param($code) Read-ExactTask { throw [Runtime.InteropServices.COMException]::new('fixture scheduler failure', $code) } 'MulticaWorker-test-Daemon' } $hresult } '*fixture scheduler failure*'
}
$missing = @(& $module { Read-ExactTask { throw [Runtime.InteropServices.COMException]::new('fixture task missing', -2147024894) } 'MulticaWorker-test-Daemon' })
Check ($missing.Count -eq 0) 'only FILE_NOT_FOUND is scheduler absence'
$found = & $module { Read-ExactTask { param($name) @{ taskName = $name } } 'MulticaWorker-test-Daemon' }
Check ($found.taskName -ceq 'MulticaWorker-test-Daemon') 'exact scheduler lookup passes its identity'
foreach ($failure in @('config', 'tasks', 'none')) {
    Setup
    $script:openedStreams = [Collections.Generic.List[IO.MemoryStream]]::new()
    $script:fileFailure = ($failure -eq 'config')
    $readers.File = {
        param($path)
        if ($script:fileFailure -and $path -ceq $script:binding.configPath) { throw 'Fixture config unavailable.' }
        $stream = [IO.MemoryStream]::new()
        $script:openedStreams.Add($stream)
        @{ path = $path; sha256 = 'A' * 64; stream = $stream }
    }
    if ($failure -eq 'tasks') { $fixture.taskError = $true }
    if ($failure -eq 'config') { Reject { Observe } '*config unavailable*' }
    elseif ($failure -eq 'tasks') { Reject { Observe } '*scheduler access denied*' }
    else { $null = Observe }
    Check (@($openedStreams | Where-Object CanRead).Count -eq 0) "file handles closed on $failure path"
}
if ($env:MULTICA_TASK_ID -or $env:MULTICA_AGENT_ID -or $env:MULTICA_TOKEN) {
    Reject { Read-WorkerHostSnapshot -Binding $binding } '*owner terminal*'
}
Setup
$fixture.fileError = $binding.releasePath
Reject { Observe @($reference) } '*file access denied*'

$scratch = Join-Path $PSScriptRoot ('.snapshot-test-' + [guid]::NewGuid().ToString('N'))
$null = [IO.Directory]::CreateDirectory($scratch)
$filePath = Join-Path $scratch 'immutable.bin'
$junction = Join-Path $scratch 'alias'
$targetDirectory = Join-Path $scratch 'target'
$targetFile = Join-Path $targetDirectory 'file.bin'
$storeDirectory = Join-Path $scratch 'store'
try {
    Setup
    $null = [IO.Directory]::CreateDirectory($storeDirectory)
    New-WorkerSimulationStore -Directory $storeDirectory -Receipt $receipt -ExecutionMode Simulation
    $storeBefore = [IO.File]::ReadAllText((Join-Path $storeDirectory 'ownership-state.json'))
    $script:registerCalls = 0; $script:maintenanceCalls = 0
    $adapters = @{ Snapshot = { (Observe).snapshot }
        Maintenance = { $script:maintenanceCalls++; throw 'Must not acquire a lease for unknown children.' }
        RegisterDisabled = { $script:registerCalls++ }; Now = { [DateTimeOffset]::UtcNow }; Checkpoint = {} }
    Reject { Invoke-WorkerStoppedTransaction -Directory $storeDirectory -Desired $next -Adapters $adapters } '*Production execution unavailable*'
    Check ($fixture.taskReads -eq 0) 'production refusal precedes snapshot callbacks'
    Reject { Invoke-WorkerStoppedTransaction -Directory $storeDirectory -Desired $next -Adapters $adapters -ExecutionMode Simulation } '*tasks_or_children_not_stopped*'
    Check ($registerCalls -eq 0 -and $maintenanceCalls -eq 0 -and
        [IO.File]::ReadAllText((Join-Path $storeDirectory 'ownership-state.json')) -ceq $storeBefore) 'snapshot-to-coordinator integration cannot mutate or journal unknown children'
    [IO.File]::WriteAllText($filePath, 'isolated fixture')
    $opened = & $module { param($p) Open-WorkerFile $p } $filePath
    try {
        Check ($opened.sha256 -ceq (Get-FileHash -LiteralPath $filePath -Algorithm SHA256).Hash) 'real read-only stream hashing'
        Reject { [IO.File]::Open($filePath, [IO.FileMode]::Open, [IO.FileAccess]::Write, [IO.FileShare]::ReadWrite).Dispose() } '*being used*'
    } finally { $opened.stream.Dispose() }
    $exclusive = [IO.FileStream]::new($filePath, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try { Reject { & $module { param($p) Open-WorkerFile $p } $filePath } '*being used*' }
    finally { $exclusive.Dispose() }
    $null = [IO.Directory]::CreateDirectory($targetDirectory)
    [IO.File]::WriteAllText($targetFile, 'junction fixture')
    $null = New-Item -ItemType Junction -Path $junction -Target $targetDirectory
    Reject { & $module { param($p) Open-WorkerFile $p } (Join-Path $junction 'file.bin') } '*Reparse-point*'
    Reject { & $module { param($p) Open-WorkerFile $p } (Join-Path $scratch 'missing.bin') } '*does not exist*'
    foreach ($path in @('relative.bin', '\\server\share\file', 'C:\fixture.bin:secret')) {
        Reject { & $module { param($p) Open-WorkerFile $p } $path } '*absolute local drive*'
    }
} finally {
    foreach ($name in @('ownership-state.json', 'ownership-state.json.lock')) {
        $path = Join-Path $storeDirectory $name
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }
    }
    if (Test-Path -LiteralPath $storeDirectory) { Remove-Item -LiteralPath $storeDirectory }
    if (Test-Path -LiteralPath $junction) { Remove-Item -LiteralPath $junction }
    if (Test-Path -LiteralPath $targetFile) { Remove-Item -LiteralPath $targetFile }
    if (Test-Path -LiteralPath $targetDirectory) { Remove-Item -LiteralPath $targetDirectory }
    if (Test-Path -LiteralPath $filePath) { Remove-Item -LiteralPath $filePath }
    Remove-Item -LiteralPath $scratch
}
Write-Output "PASS: $script:checks read-only snapshot checks; isolated XML/process/scheduler/files only."
