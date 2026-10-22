#Requires -Version 7.0
[CmdletBinding(SupportsShouldProcess)]
param(
    [string]$ConfigPath,
    [ValidateSet('Plan', 'Create', 'Prepare', 'Verify')][string]$Action = 'Plan',
    [switch]$ApproveCreate
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Fields {
    param([System.Collections.IDictionary]$Value, [string[]]$Fields, [string]$Label)
    if ($null -eq $Value) { throw "$Label must be an object." }
    foreach ($key in $Value.Keys) {
        if ($key -notin $Fields) { throw "Unknown field in ${Label}: $key. Keep credentials out of this file." }
    }
    foreach ($key in $Fields) {
        if (-not $Value.Contains($key) -or $null -eq $Value[$key] -or "$($Value[$key])".Trim() -eq '') {
            throw "Missing ${Label}.$key."
        }
    }
}

function Assert-Endpoint {
    param([string]$Value, [string]$Label, [switch]$HttpsOnly)
    $uri = $null
    if (-not [uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -or
        $uri.UserInfo -or $uri.Query -or $uri.Fragment -or
        ($uri.Scheme -ne 'https' -and ($HttpsOnly -or $uri.Scheme -ne 'http' -or -not $uri.IsLoopback))) {
        throw "$Label must be HTTPS (HTTP is allowed only on loopback), without credentials, query or fragment."
    }
}

function Read-DevBoxConfig {
    param([string]$Path)
    $c = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
    Assert-Fields $c @('schemaVersion', 'azure', 'worker') 'config'
    if ($c.schemaVersion -ne 1) { throw 'Unsupported schemaVersion.' }
    Assert-Fields $c.azure @('endpoint', 'project', 'pool', 'name') 'azure'
    Assert-Endpoint $c.azure.endpoint 'azure.endpoint' -HttpsOnly
    foreach ($key in @('project', 'pool', 'name')) {
        if ($c.azure[$key] -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$') { throw "Invalid azure.$key." }
    }
    $w = $c.worker
    Assert-Fields $w @('computerName', 'profile', 'serverUrl', 'appUrl', 'workspaceId',
        'workspacesRoot', 'maxConcurrentTasks', 'tools') 'worker'
    foreach ($key in @('computerName', 'profile')) {
        if ($w[$key] -notmatch '^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$') { throw "Invalid worker.$key." }
    }
    Assert-Endpoint $w.serverUrl 'worker.serverUrl'
    Assert-Endpoint $w.appUrl 'worker.appUrl'
    $id = [guid]::Empty
    if (-not [guid]::TryParse($w.workspaceId, [ref]$id) -or $id -eq [guid]::Empty) {
        throw 'worker.workspaceId must be a nonempty UUID.'
    }
    if ($w.workspacesRoot -notmatch '^[A-Za-z]:\\[^<>:"|?*]+$' -or $w.workspacesRoot -match '(^|\\)\.\.?($|\\)') {
        throw 'worker.workspacesRoot must be an absolute Windows directory below the drive root, without dot segments.'
    }
    if ($w.maxConcurrentTasks -isnot [long] -and $w.maxConcurrentTasks -isnot [int]) {
        throw 'worker.maxConcurrentTasks must be an integer.'
    }
    if ($w.maxConcurrentTasks -lt 1 -or $w.maxConcurrentTasks -gt 50) {
        throw 'worker.maxConcurrentTasks must be between 1 and 50.'
    }
    Assert-Fields $w.tools @('multica', 'git', 'codex', 'copilot') 'worker.tools'
    foreach ($name in $w.tools.Keys) {
        $tool = $w.tools[$name]
        Assert-Fields $tool @('path', 'version') "worker.tools.$name"
        if ($tool.path -notmatch '^[A-Za-z]:\\.+\.(exe|cmd)$' -or
            $tool.path -match '(^|\\)\.\.?($|\\)' -or
            $tool.version -notmatch '^\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?$') {
            throw "Invalid pinned path/version for $name."
        }
    }
    return $c
}

function Assert-OwnerTerminal {
    foreach ($key in @('MULTICA_AGENT_ID', 'MULTICA_TASK_ID', 'MULTICA_TASK_CONFIG_ROOT',
            'MULTICA_DAEMON_PORT', 'MULTICA_TOKEN', 'MULTICA_SERVER_URL', 'MULTICA_WORKSPACE_ID',
            'MULTICA_PROFILE', 'MULTICA_APP_URL')) {
        if ([Environment]::GetEnvironmentVariable($key)) {
            throw "$key is set. Use a clean owner terminal; do not run host provisioning from an agent task."
        }
    }
}

function Assert-WorkerHost {
    param($Worker)
    Assert-OwnerTerminal
    if (-not $IsWindows -or $env:COMPUTERNAME -ine $Worker.computerName) {
        throw "Run this stage on the intended Windows worker '$($Worker.computerName)', not this host."
    }
    foreach ($entry in Get-ChildItem Env:MULTICA_*) {
        throw "Ambient $($entry.Name) could override the manifest. Use a clean owner terminal."
    }
}

function Invoke-DevBoxCommand {
    param([string]$Executable, [string[]]$Arguments)
    $output = & $Executable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command '$Executable' failed with exit code $LASTEXITCODE. No automatic retry was attempted."
    }
    return ($output -join "`n")
}

function Get-WorkerSettings {
    param($Worker)
    return [ordered]@{
        server_url = $Worker.serverUrl
        app_url = $Worker.appUrl
        workspace_id = $Worker.workspaceId
        device_name = $Worker.computerName
        workspaces_root = $Worker.workspacesRoot
        max_concurrent_tasks = "$($Worker.maxConcurrentTasks)"
        disable_auto_update = 'true'
        disable_auto_reload = 'true'
    }
}

function Get-AzureArguments {
    param($Azure, [string]$Operation)
    $result = @('devcenter', 'dev', 'dev-box', $Operation, '--endpoint', $Azure.endpoint,
        '--project-name', $Azure.project, '--user-id', 'me', '--only-show-errors', '--output', 'json')
    if ($Operation -eq 'create') {
        $result += @('--pool-name', $Azure.pool, '--name', $Azure.name)
    }
    return $result
}

function Get-DevBoxPlan {
    param($Config)
    $w = $Config.worker
    $cli = $w.tools.multica.path
    $prefix = @('--profile', $w.profile)
    return [ordered]@{
        state = 'plan_only'
        azureList = @{ executable = 'az'; arguments = @(Get-AzureArguments $Config.azure 'list') }
        azureCreate = @{ executable = 'az'; arguments = @(Get-AzureArguments $Config.azure 'create') }
        requiredTools = $w.tools
        settings = Get-WorkerSettings $w
        ownerLogin = @{ executable = $cli; arguments = $prefix + @('login') }
        ownerStart = @{
            executable = $cli
            arguments = $prefix + @('daemon', 'start', '--no-auto-update', '--no-auto-reload',
                '--device-name', $w.computerName, '--workspaces-root', $w.workspacesRoot,
                '--max-concurrent-tasks', "$($w.maxConcurrentTasks)")
        }
        remainingGates = @('Approved Azure project/pool and cost', 'Approved image with pinned tools',
            'Owner-only network access from worker', 'Owner and provider sign-in on worker',
            'Explicit daemon-start approval', 'Approved end-to-end smoke task', 'Separate persistent-startup approval')
    }
}

function New-DevBoxWorker {
    param($Config)
    Assert-OwnerTerminal
    $extension = Invoke-DevBoxCommand 'az' @('extension', 'show', '--name', 'devcenter', '--only-show-errors', '--output', 'json') |
        ConvertFrom-Json
    if ($extension.name -ne 'devcenter') { throw 'Install the approved Azure devcenter extension before provisioning.' }
    # Never treat an authorization, network or JSON failure as "machine absent".
    $raw = Invoke-DevBoxCommand 'az' (Get-AzureArguments $Config.azure 'list')
    $boxes = ConvertFrom-Json -InputObject $raw -NoEnumerate
    if ($boxes -isnot [array]) { throw 'Azure list did not return an array; refusing to create.' }
    $existing = @($boxes | Where-Object name -EQ $Config.azure.name)
    if ($existing.Count -gt 1) { throw 'Multiple matching devboxes; refusing to create.' }
    if ($existing.Count -eq 1) {
        if ($existing[0].poolName -ine $Config.azure.pool) {
            throw 'Existing devbox belongs to a different pool; no mutation was performed.'
        }
        return @{ state = 'existing'; devbox = $existing[0] }
    }
    $created = Invoke-DevBoxCommand 'az' (Get-AzureArguments $Config.azure 'create') | ConvertFrom-Json
    if ($created.name -ine $Config.azure.name -or $created.poolName -ine $Config.azure.pool) {
        throw 'Creation returned an unexpected identity. Inspect Azure before taking further action; do not retry blindly.'
    }
    if ($created.provisioningState -in @('Failed', 'Canceled', 'Cancelled')) {
        throw "Devbox provisioning ended in '$($created.provisioningState)'. Inspect Azure; no retry was attempted."
    }
    if ($created.provisioningState -ne 'Succeeded') {
        return @{ state = 'provisioning_not_ready'; devbox = $created }
    }
    return @{ state = 'created_not_enrolled'; devbox = $created }
}

function Assert-PinnedVersion {
    param([string]$Name, [string]$Text, [string]$Expected)
    # Git for Windows appends a platform suffix to its upstream version.
    $versions = [regex]::Matches($Text, '(?<![\d.])(\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?)(?!\d|\.\d)')
    if ($Name -eq 'git') {
        $versions = [regex]::Matches($Text, '(?<![\d.])(\d+\.\d+\.\d+)(?:\.windows\.\d+)?(?!\d|\.\d|\.windows\.)')
    }
    if ($versions.Count -ne 1 -or $versions[0].Groups[1].Value -cne $Expected) {
        throw "$Name version does not match pinned version '$Expected'. No upgrade was attempted."
    }
}

function Assert-WorkerTools {
    param($Worker)
    foreach ($name in $Worker.tools.Keys) {
        $tool = $Worker.tools[$name]
        if (-not (Test-Path -LiteralPath $tool.path -PathType Leaf)) { throw "Missing $name at its pinned path." }
        $text = Invoke-DevBoxCommand $tool.path @('--version')
        Assert-PinnedVersion $name $text $tool.version
    }
}

function Get-WorkerCurrentSettings {
    param($Worker)
    $text = Invoke-DevBoxCommand $Worker.tools.multica.path @('--profile', $Worker.profile, 'config', 'show')
    $settings = @{}
    foreach ($line in $text -split "`n") {
        if ($line -match '^([a-z_]+):\s*(.*)$') { $settings[$Matches[1]] = $Matches[2].Trim() }
    }
    foreach ($key in (Get-WorkerSettings $Worker).Keys) {
        if (-not $settings.ContainsKey($key)) { throw "CLI config output missing $key; refusing to guess." }
    }
    return $settings
}

function Initialize-DevBoxWorker {
    param($Config)
    $w = $Config.worker
    Assert-WorkerHost $w
    Assert-WorkerTools $w
    $current = Get-WorkerCurrentSettings $w
    $desired = Get-WorkerSettings $w
    $changes = @()
    foreach ($key in $desired.Keys) {
        if ($current[$key] -ceq $desired[$key]) { continue }
        if ($current[$key] -ne '(not set)' -and
            -not ($key -in @('disable_auto_update', 'disable_auto_reload') -and $current[$key] -eq 'false')) {
            throw "Existing profile differs at $key. Use a fresh dedicated profile; no settings were written."
        }
        $changes += $key
    }
    if ($changes.Count -gt 0) {
        $daemon = Invoke-DevBoxCommand $w.tools.multica.path @('--profile', $w.profile, 'daemon', 'status', '--output', 'json') |
            ConvertFrom-Json
        if ($daemon.status -ne 'stopped') { throw 'Profile daemon is not stopped; no configuration changes were made.' }
        foreach ($key in $changes) {
            $null = Invoke-DevBoxCommand $w.tools.multica.path @('--profile', $w.profile, 'config', 'set', $key, $desired[$key])
        }
    }
    $actual = Get-WorkerCurrentSettings $w
    foreach ($key in $desired.Keys) {
        if ($actual[$key] -cne $desired[$key]) { throw "Configuration readback failed at $key. Inspect partial changes before retrying." }
    }
    return @{ state = 'prepared_not_enrolled'; changedKeys = $changes; plan = Get-DevBoxPlan $Config }
}

function Test-DevBoxWorker {
    param($Config)
    $w = $Config.worker
    Assert-WorkerHost $w
    Assert-WorkerTools $w
    $current = Get-WorkerCurrentSettings $w
    $desired = Get-WorkerSettings $w
    foreach ($key in $desired.Keys) {
        if ($current[$key] -cne $desired[$key]) { throw "Configuration mismatch at $key." }
    }
    $cli = $w.tools.multica.path
    $prefix = @('--profile', $w.profile)
    $daemon = Invoke-DevBoxCommand $cli ($prefix + @('daemon', 'status', '--output', 'json')) | ConvertFrom-Json
    if ($daemon.status -ne 'running' -or $daemon.device_name -ine $w.computerName -or
        $daemon.cli_version -cne $w.tools.multica.version -or -not $daemon.daemon_id) {
        throw 'Daemon is not running with the expected identity/version.'
    }
    $runtimes = Invoke-DevBoxCommand $cli ($prefix + @('--workspace-id', $w.workspaceId, 'runtime', 'list', '--output', 'json')) |
        ConvertFrom-Json -NoEnumerate
    if ($runtimes -isnot [array]) { throw 'Runtime inventory did not return an array.' }
    $ids = @{}
    foreach ($provider in @('codex', 'copilot')) {
        $matches = @($runtimes | Where-Object {
            $_.daemon_id -eq $daemon.daemon_id -and $_.workspace_id -eq $w.workspaceId -and $_.provider -eq $provider
        })
        if ($matches.Count -ne 1 -or $matches[0].status -ne 'online') { throw "$provider is not uniquely online on this daemon." }
        $rt = $matches[0]
        if (-not $rt.id) { throw "$provider registration is missing its runtime ID." }
        $seen = [DateTimeOffset]::Parse($rt.last_seen_at)
        $age = ([DateTimeOffset]::UtcNow - $seen).TotalSeconds
        if ($age -lt -30 -or $age -gt 120) { throw "$provider heartbeat is stale or clock skewed." }
        if ($rt.metadata.cli_version -cne $w.tools.multica.version -or
            $rt.device_info -notmatch [regex]::Escape($w.computerName)) {
            throw "$provider registration does not match the worker."
        }
        Assert-PinnedVersion $provider $rt.metadata.version $w.tools[$provider].version
        $ids[$provider] = $rt.id
    }
    return @{ state = 'registered_not_smoke_tested'; daemonId = $daemon.daemon_id; runtimeIds = $ids; endToEndValidated = $false }
}

if ($MyInvocation.InvocationName -ne '.') {
    if (-not $ConfigPath) { throw '-ConfigPath is required.' }
    $config = Read-DevBoxConfig $ConfigPath
    $result = switch ($Action) {
        'Plan' { Get-DevBoxPlan $config }
        'Create' {
            if (-not $ApproveCreate) { throw 'Create incurs cloud cost; explicit -ApproveCreate is required.' }
            if ($PSCmdlet.ShouldProcess($config.azure.name, 'Create devbox in the approved existing pool')) {
                New-DevBoxWorker $config
            }
        }
        'Prepare' {
            if ($PSCmdlet.ShouldProcess($config.worker.computerName, 'Prepare dedicated local worker profile')) {
                Initialize-DevBoxWorker $config
            }
        }
        'Verify' { Test-DevBoxWorker $config }
    }
    if ($null -ne $result) { $result | ConvertTo-Json -Depth 12 }
}
