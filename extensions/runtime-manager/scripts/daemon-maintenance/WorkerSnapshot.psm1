#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-TextHash([string]$Text) {
    $hash = [Security.Cryptography.SHA256]::Create()
    try { [BitConverter]::ToString($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text))).Replace('-', '') }
    finally { $hash.Dispose() }
}

function Get-WorkerDefinition {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Xml, [Parameter(Mandatory)][string]$TaskName,
        [Parameter(Mandatory)][string]$OwnerSid)
    if ($TaskName -cnotmatch '^MulticaWorker-[A-Za-z0-9_-]+-(Tunnel|Daemon)$' -or $OwnerSid -cnotmatch '^S-1-\d+(?:-\d+)+$') {
        throw 'Invalid exact task name/owner SID.'
    }
    if ($Xml.Length -gt 1MB) { throw 'Task XML exceeds 1 MiB.' }
    $settings = [Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $settings.MaxCharactersInDocument = 1MB
    $reader = [Xml.XmlReader]::Create([IO.StringReader]::new($Xml), $settings)
    $document = [Xml.XmlDocument]::new()
    $document.PreserveWhitespace = $true
    $document.XmlResolver = $null
    try { $document.Load($reader) } finally { $reader.Dispose() }
    $ns = 'http://schemas.microsoft.com/windows/2004/02/mit/task'
    if ($document.DocumentElement.LocalName -cne 'Task' -or $document.DocumentElement.NamespaceURI -cne $ns) {
        throw 'Unexpected task XML root/namespace.'
    }
    $manager = [Xml.XmlNamespaceManager]::new($document.NameTable)
    $manager.AddNamespace('t', $ns)
    foreach ($xpath in @('/t:Task/t:Settings', '/t:Task/t:Settings/t:Enabled',
        '/t:Task/t:Principals', '/t:Task/t:Principals/t:Principal',
        '/t:Task/t:Principals/t:Principal/t:UserId')) {
        if ($document.SelectNodes($xpath, $manager).Count -ne 1) { throw "Task XML requires exactly one $xpath." }
    }
    if ($document.SelectSingleNode('/t:Task/t:Principals/t:Principal/t:UserId', $manager).InnerText -cne $OwnerSid -or
        $document.SelectNodes('//t:GroupId', $manager).Count) { throw 'Task principal SID does not match the expected owner.' }
    $enabledText = $document.SelectSingleNode('/t:Task/t:Settings/t:Enabled', $manager).InnerText
    if ($enabledText -cnotin @('true', 'false')) { throw 'Task Enabled must be explicit true/false.' }
    $uris = $document.SelectNodes('/t:Task/t:RegistrationInfo/t:URI', $manager)
    if ($uris.Count -gt 1 -or ($uris.Count -eq 1 -and $uris[0].InnerText -cne "\$TaskName")) {
        throw 'Task XML URI does not match the exact root identity.'
    }
    # Expanded names and ordinal attribute sorting remove prefix/format noise;
    # child order and all leaf text remain significant (including action argv).
    $canonical = [Text.StringBuilder]::new('worker-task-xml-v1')
    function Add-Token([string]$Value) {
        $null = $canonical.Append($Value.Length.ToString([Globalization.CultureInfo]::InvariantCulture)).Append(':').Append($Value)
    }
    function Add-Element([Xml.XmlElement]$Element, [int]$Depth) {
        if ($Depth -gt 64) { throw 'Task XML exceeds depth 64.' }
        Add-Token 'element'; Add-Token $Element.NamespaceURI; Add-Token $Element.LocalName
        $attributes = [Collections.Generic.SortedDictionary[string,string]]::new([StringComparer]::Ordinal)
        foreach ($attribute in $Element.Attributes) {
            if ($attribute.NamespaceURI -ceq 'http://www.w3.org/2000/xmlns/') { continue }
            $key = "{$($attribute.NamespaceURI)}$($attribute.LocalName)"
            $attributes.Add($key, $attribute.Value)
        }
        Add-Token ($attributes.Count.ToString([Globalization.CultureInfo]::InvariantCulture))
        foreach ($key in $attributes.Keys) { Add-Token $key; Add-Token $attributes[$key] }
        $hasElements = @($Element.ChildNodes | Where-Object NodeType -EQ Element).Count -gt 0
        if (-not $hasElements) { Add-Token 'text'; Add-Token $Element.InnerText }
        foreach ($node in $Element.ChildNodes) {
            switch ($node.NodeType.ToString()) {
                Element { Add-Element $node ($Depth + 1) }
                Comment { }
                ProcessingInstruction { throw 'Processing instructions are not supported in task XML.' }
                default {
                    if ($hasElements -and -not [string]::IsNullOrWhiteSpace($node.Value)) {
                        throw 'Mixed task XML content is not supported.'
                    }
                }
            }
        }
        Add-Token 'end'
    }
    if ($document.SelectNodes('//processing-instruction()').Count) { throw 'Processing instructions are not supported in task XML.' }
    Add-Element $document.DocumentElement 0
    @{ taskPath = '\'; taskName = $TaskName; originalXml = $Xml; enabled = ($enabledText -ceq 'true')
        definitionSha256 = (Get-TextHash $canonical.ToString()); hashAlgorithm = 'worker-task-xml-v1' }
}

function Open-WorkerFile([string]$Path) {
    if (-not [IO.Path]::IsPathFullyQualified($Path) -or $Path -notmatch '^[A-Za-z]:\\' -or
        $Path.Substring(2).Contains(':')) { throw 'Snapshot files require absolute local drive paths without alternate streams.' }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $item = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($item -isnot [IO.FileInfo]) { throw 'Snapshot file is not a regular file.' }
    $ancestor = $item
    while ($null -ne $ancestor) {
        if ($ancestor.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse-point snapshot files are refused.' }
        $ancestor = if ($ancestor -is [IO.FileInfo]) { $ancestor.Directory } else { $ancestor.Parent }
    }
    # Deny cooperating writes/deletion while all snapshot reads are collected.
    $stream = [IO.FileStream]::new($fullPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $hash = [Security.Cryptography.SHA256]::Create()
        try { $digest = [BitConverter]::ToString($hash.ComputeHash($stream)).Replace('-', '') } finally { $hash.Dispose() }
        @{ path = $fullPath; sha256 = $digest; stream = $stream }
    } catch { $stream.Dispose(); throw }
}

function Assert-SnapshotBinding($Binding) {
    foreach ($key in @('host', 'profile', 'ownerSid', 'releasePath', 'configPath', 'releaseSha256', 'configSha256')) {
        if ($Binding[$key] -isnot [string] -or [string]::IsNullOrWhiteSpace($Binding[$key])) { throw "Missing snapshot binding: $key." }
    }
    if ($Binding.profile -cnotmatch '^[A-Za-z0-9_-]+$' -or $Binding.ownerSid -cnotmatch '^S-1-\d+(?:-\d+)+$' -or
        $Binding.releaseSha256 -cnotmatch '^[A-Fa-f0-9]{64}$' -or $Binding.configSha256 -cnotmatch '^[A-Fa-f0-9]{64}$') {
        throw 'Invalid snapshot profile/SID/hash binding.'
    }
}

function Get-WorkerProcessEvidence($Reference, [object[]]$Processes, $Binding, [scriptblock]$ReadImage) {
    foreach ($key in @('taskName', 'creationTimeUtc', 'imagePath', 'imageSha256')) {
        if ($Reference[$key] -isnot [string] -or [string]::IsNullOrWhiteSpace($Reference[$key])) { throw "Invalid process reference: $key." }
    }
    if (($Reference.pid -isnot [int] -and $Reference.pid -isnot [long]) -or $Reference.pid -lt 1 -or $Reference.pid -gt [uint32]::MaxValue -or
        $Reference.creationTimeUtc -cnotmatch '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,7})?Z$' -or
        $Reference.imageSha256 -cnotmatch '^[A-Fa-f0-9]{64}$' -or $Reference.imagePath -notmatch '^[A-Za-z]:\\') {
        throw 'Invalid process PID/creation/image/hash reference.'
    }
    $expectedTime = [DateTimeOffset]::Parse($Reference.creationTimeUtc, [Globalization.CultureInfo]::InvariantCulture)
    $actual = @($Processes | Where-Object { $_.pid -eq $Reference.pid })
    $state = 'unknown'; $reason = 'pid_not_observed'
    $observedIdentity = $null
    if ($actual.Count -gt 1) { throw 'Duplicate PID in process census.' }
    if ($actual.Count -eq 1) {
        $process = $actual[0]
        $observedIdentity = @{ creationTimeUtc = $process.creationTimeUtc; imagePath = $process.imagePath
            ownerSid = $process.ownerSid; imageSha256 = $null }
        $reason = 'process_identity_unavailable'
        if ($process.creationTimeUtc -and $process.imagePath -and $process.ownerSid) {
            if ([DateTimeOffset]::Parse($process.creationTimeUtc, [Globalization.CultureInfo]::InvariantCulture) -ne $expectedTime) {
                $reason = 'pid_reused'
            } elseif ($process.ownerSid -cne $Binding.ownerSid -or
                -not [string]::Equals($process.imagePath, $Reference.imagePath, [StringComparison]::OrdinalIgnoreCase)) {
                $reason = 'process_identity_mismatch'
            } else {
                $image = & $ReadImage $process.imagePath
                $observedIdentity.imageSha256 = $image.sha256
                if ($image.sha256 -ine $Reference.imageSha256) { $reason = 'process_image_drift' }
                else { $state = 'running'; $reason = 'pid_creation_image_match' }
            }
        }
    }
    @{ taskName = $Reference.taskName; pid = $Reference.pid; state = $state; reasonCode = $reason
        observedIdentity = $observedIdentity }
}

function Read-WorkerSnapshotCore($Binding, [object[]]$ProcessReferences, [System.Collections.IDictionary]$Readers) {
    Assert-SnapshotBinding $Binding
    $identity = & $Readers.Identity
    if (-not [string]::Equals($identity.host, $Binding.host, [StringComparison]::OrdinalIgnoreCase) -or
        $identity.ownerSid -cne $Binding.ownerSid) { throw 'Snapshot host/owner binding mismatch.' }
    $files = [Collections.Generic.List[object]]::new()
    try {
        foreach ($key in @('release', 'config')) { $files.Add((& $Readers.File $Binding["${key}Path"])) }
        $definitions = @(); $tasks = @(); $processEvidence = @()
        $names = @('Tunnel', 'Daemon' | ForEach-Object { "MulticaWorker-$($Binding.profile)-$_" })
        $referencePids = [Collections.Generic.HashSet[long]]::new()
        foreach ($reference in $ProcessReferences) {
            if ($reference.taskName -cnotin $names) { throw 'Process reference belongs to a different profile/task.' }
            if (-not $referencePids.Add($reference.pid)) { throw 'Duplicate process reference PID.' }
        }
        $inventory = @(& $Readers.Tasks)
        $processes = @(& $Readers.Processes)
        foreach ($reference in $ProcessReferences) {
            $processEvidence += Get-WorkerProcessEvidence $reference $processes $Binding {
                param($path)
                $image = & $Readers.File $path
                $files.Add($image)
                $image
            }
        }
        foreach ($name in $names) {
            $matches = @($inventory | Where-Object { $_.taskPath -ceq '\' -and $_.taskName -ceq $name })
            if ($matches.Count -gt 1) { throw 'Duplicate exact task identity.' }
            if ($matches.Count -eq 0) { continue }
            $task = $matches[0]
            $definition = Get-WorkerDefinition -Xml $task.xml -TaskName $name -OwnerSid $Binding.ownerSid
            if ($task.enabled -isnot [bool] -or $task.enabled -ne $definition.enabled) { throw 'Scheduler/XML enabled state disagrees.' }
            $definitions += $definition
            $childState = if (@($processEvidence | Where-Object { $_.taskName -ceq $name -and $_.state -ceq 'running' }).Count) { 'running' } else { 'unknown' }
            # Disabled/Ready is not proof of absent orphaned children. Without a
            # fenced durable child tracker this adapter never emits stopped.
            $state = switch ($task.state) { 4 { 'running' } 1 { 'stopped' } 3 { 'stopped' } default { 'unknown' } }
            $tasks += @{ taskPath = '\'; taskName = $name; definitionSha256 = $definition.definitionSha256
                state = $state; childState = $childState }
        }
        $after = @(& $Readers.Tasks)
        foreach ($name in $names) {
            $beforeTask = @($inventory | Where-Object { $_.taskPath -ceq '\' -and $_.taskName -ceq $name })
            $afterTask = @($after | Where-Object { $_.taskPath -ceq '\' -and $_.taskName -ceq $name })
            if ($beforeTask.Count -ne $afterTask.Count) { throw 'Task inventory changed during observation.' }
            if ($afterTask.Count -eq 1) {
                $again = Get-WorkerDefinition $afterTask[0].xml $name $Binding.ownerSid
                $first = @($definitions | Where-Object taskName -CEQ $name)[0]
                if ($again.definitionSha256 -cne $first.definitionSha256 -or $afterTask[0].state -ne $beforeTask[0].state -or
                    $afterTask[0].enabled -ne $beforeTask[0].enabled) { throw 'Task definition/state changed during observation.' }
            }
        }
        @{ snapshot = @{ host = $identity.host; ownerSid = $identity.ownerSid; profile = $Binding.profile
                releaseSha256 = $files[0].sha256; configSha256 = $files[1].sha256; tasks = @($tasks); journalPending = $false }
            definitions = @($definitions); processes = @($processEvidence)
            filesMatchExpected = ($files[0].sha256 -ieq $Binding.releaseSha256 -and $files[1].sha256 -ieq $Binding.configSha256)
            observedAtUtc = [DateTimeOffset]::UtcNow.ToString('o'); childCoverage = 'unproven'
            authorizesMutation = $false; productionEnabled = $false }
    } finally {
        foreach ($file in $files) { if ($file.Contains('stream')) { $file.stream.Dispose() } }
    }
}

function Read-ExactTask([scriptblock]$GetTask, [string]$Name) {
    try { & $GetTask $Name }
    catch {
        # Only the exact scheduler lookup's FILE_NOT_FOUND means absent.
        # In particular, E_ACCESSDENIED is not an empty/missing task.
        $exception = $_.Exception.GetBaseException()
        if ($exception.HResult -ne -2147024894) { throw }
    }
}

function Read-WorkerHostSnapshot {
    [CmdletBinding()]
    param([Parameter(Mandatory)][System.Collections.IDictionary]$Binding,
        [object[]]$ProcessReferences = @())
    foreach ($key in @('MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_TASK_CONFIG_ROOT', 'MULTICA_DAEMON_PORT',
        'MULTICA_TOKEN', 'MULTICA_SERVER_URL', 'MULTICA_WORKSPACE_ID', 'MULTICA_PROFILE', 'MULTICA_APP_URL')) {
        if ([Environment]::GetEnvironmentVariable($key)) { throw 'Host snapshots require an owner terminal, not a managed task.' }
    }
    if (-not $IsWindows) { throw 'Host snapshot collection requires Windows.' }
    # Exact lookups avoid treating access-filtered enumeration as absence.
    function Read-RootTasks {
        $service = New-Object -ComObject 'Schedule.Service'
        try {
            $service.Connect()
            $folder = $service.GetFolder('\')
            try {
                foreach ($name in @("MulticaWorker-$($Binding.profile)-Tunnel", "MulticaWorker-$($Binding.profile)-Daemon")) {
                    $task = Read-ExactTask { param($n) $folder.GetTask($n) } $name
                    if ($null -eq $task) { continue }
                    try {
                        if ($task.Path -cne "\$name" -or $task.Name -cne $name) { throw 'Scheduler returned a different task identity.' }
                        @{ taskPath = '\'; taskName = [string]$task.Name; xml = [string]$task.Xml
                            enabled = [bool]$task.Enabled; state = [int]$task.State }
                    } finally { $null = [Runtime.InteropServices.Marshal]::FinalReleaseComObject($task) }
                }
            } finally { $null = [Runtime.InteropServices.Marshal]::FinalReleaseComObject($folder) }
        } finally { $null = [Runtime.InteropServices.Marshal]::FinalReleaseComObject($service) }
    }
    $readers = @{
        Identity = {
            $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
            try { @{ host = [Environment]::MachineName; ownerSid = $identity.User.Value } }
            finally { $identity.Dispose() }
        }
        File = { param($path) Open-WorkerFile $path }
        Tasks = { Read-RootTasks }
        Processes = {
            foreach ($process in @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)) {
                # Only inspect owner/image access for requested identities.
                if ($process.ProcessId -notin @($ProcessReferences | ForEach-Object { $_.pid })) { continue }
                $owner = Invoke-CimMethod -InputObject $process -MethodName GetOwnerSid -ErrorAction Stop
                if ($owner.ReturnValue -ne 0) { throw "Process owner observation failed: $($owner.ReturnValue)." }
                @{ pid = [long]$process.ProcessId; creationTimeUtc = $(if ($process.CreationDate) { $process.CreationDate.ToUniversalTime().ToString('o') } else { $null })
                    imagePath = $process.ExecutablePath; ownerSid = $owner.Sid }
            }
        }
    }
    Read-WorkerSnapshotCore $Binding $ProcessReferences $readers
}

Export-ModuleMember -Function Get-WorkerDefinition, Read-WorkerHostSnapshot
