#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:passed = 0

. (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1')
$examplePath = Join-Path $PSScriptRoot 'devbox.example.json'
$template = Get-Content -LiteralPath $examplePath -Raw

function Assert-Equal {
    param($Actual, $Expected)
    if (($Actual | ConvertTo-Json -Depth 15 -Compress) -cne ($Expected | ConvertTo-Json -Depth 15 -Compress)) {
        throw "Expected '$Expected', got '$Actual'."
    }
}

function Assert-Throws {
    param([scriptblock]$Body, [string]$Pattern)
    $caught = $null
    try { & $Body | Out-Null } catch { $caught = $_ }
    if ($null -eq $caught -or $caught.Exception.Message -notmatch $Pattern) {
        throw "Expected failure matching '$Pattern'; got '$caught'."
    }
}

function Test-Case {
    param([string]$Name, [scriptblock]$Body)
    & $Body
    $script:passed++
    Write-Host "PASS $Name"
}

function New-TestConfig { return ($template | ConvertFrom-Json -AsHashtable) }

Test-Case 'script parses and default plan is offline in an agent context' {
    $errors = $null
    [Management.Automation.Language.Parser]::ParseFile(
        (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1'), [ref]$null, [ref]$errors) | Out-Null
    Assert-Equal @($errors).Count 0
    $plan = & (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1') -ConfigPath $examplePath | ConvertFrom-Json
    Assert-Equal $plan.state 'plan_only'
    Assert-Equal $plan.azureCreate.arguments[3] 'create'
    Assert-Equal ($plan.azureCreate.arguments -contains '--no-wait') $false
    Assert-Equal ($plan.ownerStart.arguments -contains '--no-auto-reload') $true
}

Test-Case 'WhatIf avoids both cloud and host side effects' {
    function Invoke-DevBoxCommand { throw 'External command must not run' }
    & (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1') -ConfigPath $examplePath -Action Create -ApproveCreate -WhatIf
    & (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1') -ConfigPath $examplePath -Action Prepare -WhatIf
    Assert-Throws { & (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1') -ConfigPath $examplePath -Action Create } 'ApproveCreate'
}

Test-Case 'manifest rejects secrets, bad identifiers, URLs, paths and capacity' {
    $fixtures = Join-Path $PSScriptRoot ('.test-' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $fixtures
    try {
        $cases = @(
            @{ Change = { param($c) $c.token = 'not-a-real-secret' }; Error = 'Unknown field' },
            @{ Change = { param($c) $c.azure.endpoint = 'http://example.invalid' }; Error = 'HTTPS' },
            @{ Change = { param($c) $c.worker.serverUrl = 'https://user:password@example.invalid' }; Error = 'HTTPS' },
            @{ Change = { param($c) $c.worker.appUrl = 'https://example.invalid?token=not-real' }; Error = 'HTTPS' },
            @{ Change = { param($c) $c.worker.profile = '..\other' }; Error = 'profile' },
            @{ Change = { param($c) $c.worker.workspaceId = 'not-a-uuid' }; Error = 'UUID' },
            @{ Change = { param($c) $c.worker.workspacesRoot = 'C:\' }; Error = 'absolute Windows' },
            @{ Change = { param($c) $c.worker.workspacesRoot = 'C:\worker\..\other' }; Error = 'dot segments' },
            @{ Change = { param($c) $c.worker.maxConcurrentTasks = 0 }; Error = 'between 1 and 50' },
            @{ Change = { param($c) $c.worker.maxConcurrentTasks = 2.5 }; Error = 'integer' },
            @{ Change = { param($c) $c.worker.tools.codex.version = 'latest' }; Error = 'pinned' },
            @{ Change = { param($c) $c.worker.tools.copilot.path = 'copilot.cmd' }; Error = 'pinned' }
        )
        foreach ($case in $cases) {
            $c = New-TestConfig
            & $case.Change $c
            $file = Join-Path $fixtures 'config.json'
            $c | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $file -Encoding utf8
            Assert-Throws { Read-DevBoxConfig $file } $case.Error
        }
    } finally {
        Remove-Item -LiteralPath (Join-Path $fixtures 'config.json')
        Remove-Item -LiteralPath $fixtures
    }
}

Test-Case 'owner guard rejects agent context without invoking host commands' {
    # No environment variables are cleared to exercise this test.
    if ($env:MULTICA_TASK_ID -or $env:MULTICA_AGENT_ID -or $env:MULTICA_TOKEN) {
        Assert-Throws { Assert-OwnerTerminal } 'clean owner terminal'
    } else {
        function Assert-OwnerTerminal { throw 'simulated task context' }
        Assert-Throws { New-DevBoxWorker (New-TestConfig) } 'simulated task context'
    }
}

Test-Case 'wrong worker cannot prepare a profile' {
    function Assert-OwnerTerminal {}
    $c = New-TestConfig
    $c.worker.computerName = 'definitely-not-this-worker'
    Assert-Throws { Assert-WorkerHost $c.worker } 'intended Windows worker'
}

Test-Case 'Azure creates only after a successful empty inventory' {
    function Assert-OwnerTerminal {}
    $script:calls = @()
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[0] -eq 'extension') { return '{"name":"devcenter"}' }
        $script:calls += ,@($Executable, $Arguments)
        if ($Arguments[3] -eq 'list') { return '[]' }
        return '{"name":"multica-worker-03","poolName":"REPLACE-approved-pool","provisioningState":"Succeeded"}'
    }
    $r = New-DevBoxWorker (New-TestConfig)
    Assert-Equal $r.state 'created_not_enrolled'
    Assert-Equal $script:calls.Count 2
    Assert-Equal $script:calls[0][1][3] 'list'
    Assert-Equal $script:calls[1][1][3] 'create'
}

Test-Case 'Azure reuses existing machine and detects pool conflict' {
    function Assert-OwnerTerminal {}
    $script:pool = 'REPLACE-approved-pool'
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[0] -eq 'extension') { return '{"name":"devcenter"}' }
        Assert-Equal $Arguments[3] 'list'
        return "[{`"name`":`"MULTICA-WORKER-03`",`"poolName`":`"$script:pool`"}]"
    }
    Assert-Equal (New-DevBoxWorker (New-TestConfig)).state 'existing'
    $script:pool = 'another-pool'
    Assert-Throws { New-DevBoxWorker (New-TestConfig) } 'different pool'
}

Test-Case 'inventory failures and malformed replies never trigger creation' {
    function Assert-OwnerTerminal {}
    $script:reply = '{}'
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[0] -eq 'extension') { return '{"name":"devcenter"}' }
        Assert-Equal $Arguments[3] 'list'
        if ($script:reply -eq 'denied') { throw '403 access denied' }
        return $script:reply
    }
    foreach ($reply in @('{}', 'null', 'not json', 'denied')) {
        $script:reply = $reply
        Assert-Throws { New-DevBoxWorker (New-TestConfig) } 'array|JSON|denied'
    }
}

Test-Case 'create errors and nonterminal provisioning are not reported as completed' {
    function Assert-OwnerTerminal {}
    $script:state = 'PendingApproval'
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[0] -eq 'extension') { return '{"name":"devcenter"}' }
        if ($Arguments[3] -eq 'list') { return '[]' }
        if ($script:state -eq 'denied') { throw '403 denied during create' }
        return "{`"name`":`"multica-worker-03`",`"poolName`":`"REPLACE-approved-pool`",`"provisioningState`":`"$script:state`"}"
    }
    Assert-Equal (New-DevBoxWorker (New-TestConfig)).state 'provisioning_not_ready'
    foreach ($state in @('Failed', 'Canceled', 'denied')) {
        $script:state = $state
        Assert-Throws { New-DevBoxWorker (New-TestConfig) } 'ended in|403 denied'
    }
}

Test-Case 'missing Azure extension fails before any devbox command' {
    function Assert-OwnerTerminal {}
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        Assert-Equal $Arguments[0] 'extension'
        throw 'devcenter extension is not installed'
    }
    Assert-Throws { New-DevBoxWorker (New-TestConfig) } 'not installed'
}

Test-Case 'pinned versions handle provider and Git output and reject ambiguity' {
    Assert-PinnedVersion 'copilot' 'GitHub Copilot CLI 1.0.101.' '1.0.101'
    Assert-PinnedVersion 'codex' 'codex-cli 0.163.0' '0.163.0'
    Assert-PinnedVersion 'git' 'git version 2.51.0.windows.1' '2.51.0'
    Assert-Throws { Assert-PinnedVersion 'codex' 'codex-cli 0.161.0' '0.163.0' } 'does not match'
    Assert-Throws { Assert-PinnedVersion 'copilot' '1.0.101 and 1.0.102' '1.0.101' } 'does not match'
    Assert-Throws { Assert-PinnedVersion 'codex' 'unrecognized' '0.163.0' } 'does not match'
    Assert-Throws { Assert-PinnedVersion 'git' 'git version 2.51.0.2' '2.51.0' } 'does not match'
}

Test-Case 'missing tools fail before version invocation' {
    function Test-Path { return $false }
    function Invoke-DevBoxCommand { throw 'Should not run' }
    Assert-Throws { Assert-WorkerTools (New-TestConfig).worker } 'Missing'
}

Test-Case 'Prepare writes missing settings, verifies readback, then becomes a no-op' {
    function Assert-WorkerHost {}
    function Assert-WorkerTools {}
    $c = New-TestConfig
    $script:settings = @{}
    foreach ($key in (Get-WorkerSettings $c.worker).Keys) { $script:settings[$key] = '(not set)' }
    $script:writes = 0
    function Get-WorkerCurrentSettings { return $script:settings }
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[2] -eq 'daemon') { return '{"status":"stopped"}' }
        Assert-Equal $Arguments[2] 'config'
        $script:settings[$Arguments[4]] = $Arguments[5]
        $script:writes++
    }
    Assert-Equal (Initialize-DevBoxWorker $c).state 'prepared_not_enrolled'
    Assert-Equal $script:writes 8
    Assert-Equal (Initialize-DevBoxWorker $c).changedKeys.Count 0
    Assert-Equal $script:writes 8
}

Test-Case 'Prepare detects all conflicts before first write' {
    function Assert-WorkerHost {}
    function Assert-WorkerTools {}
    $c = New-TestConfig
    $script:settings = Get-WorkerSettings $c.worker
    $script:settings.server_url = '(not set)'
    $script:settings.workspace_id = 'another-workspace'
    function Get-WorkerCurrentSettings { return $script:settings }
    function Invoke-DevBoxCommand { throw 'No external command allowed after conflict' }
    Assert-Throws { Initialize-DevBoxWorker $c } 'differs at workspace_id'
}

Test-Case 'Prepare refuses active daemons and propagates partial write failure' {
    function Assert-WorkerHost {}
    function Assert-WorkerTools {}
    $script:settings = @{}
    $c = New-TestConfig
    foreach ($key in (Get-WorkerSettings $c.worker).Keys) { $script:settings[$key] = '(not set)' }
    $script:status = 'running'
    function Get-WorkerCurrentSettings { return $script:settings }
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[2] -eq 'daemon') { return "{`"status`":`"$script:status`"}" }
        throw 'disk full during config set'
    }
    Assert-Throws { Initialize-DevBoxWorker $c } 'not stopped'
    $script:status = 'stopped'
    Assert-Throws { Initialize-DevBoxWorker $c } 'disk full'
}

Test-Case 'Prepare can resume matching partial writes and detects a failed readback' {
    function Assert-WorkerHost {}
    function Assert-WorkerTools {}
    $c = New-TestConfig
    $script:settings = Get-WorkerSettings $c.worker
    $script:settings.server_url = '(not set)'
    function Get-WorkerCurrentSettings { return $script:settings }
    $script:persist = $true
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments[2] -eq 'daemon') { return '{"status":"stopped"}' }
        if ($script:persist) { $script:settings[$Arguments[4]] = $Arguments[5] }
    }
    Assert-Equal (Initialize-DevBoxWorker $c).changedKeys @('server_url')
    $script:settings.server_url = '(not set)'
    $script:persist = $false
    Assert-Throws { Initialize-DevBoxWorker $c } 'readback failed'
}

Test-Case 'config parser fails closed on missing fields' {
    function Invoke-DevBoxCommand { return 'server_url: https://example.invalid' }
    Assert-Throws { Get-WorkerCurrentSettings (New-TestConfig).worker } 'missing'
}

Test-Case 'Verify binds evidence to daemon, provider, workspace, versions and fresh heartbeat' {
    function Assert-WorkerHost {}
    function Assert-WorkerTools {}
    $c = New-TestConfig
    $script:settings = Get-WorkerSettings $c.worker
    function Get-WorkerCurrentSettings { return $script:settings }
    $script:daemon = @{ status = 'running'; device_name = $c.worker.computerName; cli_version = '0.6.3'; daemon_id = 'test-daemon' }
    $script:runtimes = @()
    foreach ($provider in @('codex', 'copilot')) {
        $script:runtimes += @{
            id = "$provider-runtime"; daemon_id = 'test-daemon'; workspace_id = $c.worker.workspaceId
            provider = $provider; status = 'online'; last_seen_at = [DateTimeOffset]::UtcNow.ToString('o')
            device_info = $c.worker.computerName
            metadata = @{ cli_version = '0.6.3'; version = "$provider $($c.worker.tools[$provider].version)" }
        }
    }
    function Invoke-DevBoxCommand {
        param($Executable, $Arguments)
        if ($Arguments -contains 'status') { return ($script:daemon | ConvertTo-Json) }
        return (ConvertTo-Json -InputObject $script:runtimes -Depth 5)
    }
    $r = Test-DevBoxWorker $c
    Assert-Equal $r.state 'registered_not_smoke_tested'
    Assert-Equal $r.endToEndValidated $false
    Assert-Equal $r.runtimeIds.codex 'codex-runtime'
    $script:runtimes[0].last_seen_at = [DateTimeOffset]::UtcNow.AddSeconds(-121).ToString('o')
    Assert-Throws { Test-DevBoxWorker $c } 'heartbeat'
    $script:runtimes[0].last_seen_at = [DateTimeOffset]::UtcNow.AddSeconds(31).ToString('o')
    Assert-Throws { Test-DevBoxWorker $c } 'clock skewed'
    $script:runtimes[0].last_seen_at = [DateTimeOffset]::UtcNow.ToString('o')
    $script:runtimes[0].metadata.version = 'codex 0.161.0'
    Assert-Throws { Test-DevBoxWorker $c } 'does not match'
    $script:runtimes[0].metadata.version = 'codex 0.163.0'
    $script:runtimes[0].daemon_id = 'another-daemon'
    Assert-Throws { Test-DevBoxWorker $c } 'uniquely online'
    $script:runtimes[0].daemon_id = 'test-daemon'
    $script:runtimes[0].workspace_id = [guid]::NewGuid().ToString()
    Assert-Throws { Test-DevBoxWorker $c } 'uniquely online'
    $script:runtimes[0].workspace_id = $c.worker.workspaceId
    $script:runtimes[0].status = 'offline'
    Assert-Throws { Test-DevBoxWorker $c } 'uniquely online'
    $script:runtimes[0].status = 'online'
    $script:runtimes += $script:runtimes[0]
    Assert-Throws { Test-DevBoxWorker $c } 'uniquely online'
}

Test-Case 'native command failures are not success-shaped output' {
    $pwsh = (Get-Process -Id $PID).Path
    Assert-Throws { Invoke-DevBoxCommand $pwsh @('-NoProfile', '-Command', 'exit 7') } 'exit code 7'
}

Write-Host "$script:passed test cases passed."
