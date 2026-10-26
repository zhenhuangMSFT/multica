$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'WorkerPolicy.ps1')
$installer = Join-Path $PSScriptRoot 'Install-WorkerLogon.ps1'
$config = Get-Content (Join-Path $PSScriptRoot 'pilot-remote.example.json') -Raw | ConvertFrom-Json
$scratch = Join-Path $PSScriptRoot ('policy-test-' + [guid]::NewGuid().ToString('N') + '.json')
$checks = 0
function Expect-Rejection([scriptblock]$Action, [string]$Pattern) {
    $rejected = $false
    try { & $Action | Out-Null } catch { $rejected = $_.Exception.Message -like $Pattern }
    if (-not $rejected) { throw "Expected rejection: $Pattern" }
}
try {
    $config.DeviceName = [Environment]::MachineName.ToLowerInvariant()
    Assert-WorkerTargetHost $config
    $checks++
    $config.DeviceName = 'different-' + [Environment]::MachineName
    Expect-Rejection { Assert-WorkerTargetHost $config } '*Target host mismatch*'
    $checks++
    $config | ConvertTo-Json | Set-Content -LiteralPath $scratch
    # These calls must fail before identity lookup, locks or scheduler access,
    # including WhatIf; no task identity is cleared and no host cmdlet is invoked.
    foreach ($mode in @('Install','Remove')) {
        Expect-Rejection { & $installer -ConfigPath $scratch -Mode $mode } '*Target host mismatch*'
        Expect-Rejection { & $installer -ConfigPath $scratch -Mode $mode -WhatIf } '*Target host mismatch*'
        $checks += 2
    }
    $plan = & $installer -ConfigPath $scratch -Mode Plan | ConvertFrom-Json
    if ($plan.Count -ne 2) { throw 'Offline cross-host planning must remain supported.' }
    $checks++
    foreach ($invalid in @('', ' ', ([Environment]::MachineName + '.example.test'))) {
        $config.DeviceName = $invalid
        Expect-Rejection { Assert-WorkerTargetHost $config } '*Target host mismatch*'
        $checks++
    }
    $config.DeviceName = [Environment]::MachineName
    $config.WorkspacesRoot = 'C:\Owner workspace\existing'
    $arguments = @(Get-WorkerDaemonArguments $config)
    foreach ($flag in @('--no-auto-update','--no-auto-reload')) {
        if (@($arguments | Where-Object { $_ -ceq $flag }).Count -ne 1) { throw "Missing or duplicated policy flag: $flag" }
        $checks++
    }
    foreach ($pair in @(@('--profile','pilot-remote'), @('--workspaces-root',$config.WorkspacesRoot), @('--max-concurrent-tasks','1'), @('--device-name',$config.DeviceName))) {
        $index = [Array]::IndexOf($arguments, $pair[0])
        if ($index -lt 0 -or $arguments[$index + 1] -cne $pair[1]) { throw "Lost existing launch setting: $($pair[0])" }
        $checks++
    }
    $ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$null, [ref]$null)
    $schedulerCalls = $ast.FindAll({
        param($node)
        $node -is [Management.Automation.Language.CommandAst] -and
            $node.GetCommandName() -in @('Get-ScheduledTask', 'Register-ScheduledTask', 'Unregister-ScheduledTask')
    }, $true)
    foreach ($call in $schedulerCalls) {
        $elements = $call.CommandElements
        $pathIndex = -1
        for ($i = 0; $i -lt $elements.Count; $i++) {
            if ($elements[$i] -is [Management.Automation.Language.CommandParameterAst] -and
                $elements[$i].ParameterName -eq 'TaskPath') { $pathIndex = $i }
        }
        if ($pathIndex -lt 0 -or $elements[$pathIndex + 1].SafeGetValue() -cne '\') {
            throw 'Every scheduler operation must bind the exact root TaskPath.'
        }
        $checks++
    }
    Write-Output "PASS: $checks isolated host/policy checks; no host mutation or child process launch."
} finally {
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch }
}
