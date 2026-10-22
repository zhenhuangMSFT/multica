$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot 'Install-WorkerLogon.ps1'
$configPath = Join-Path $PSScriptRoot 'pilot-remote.example.json'
foreach ($file in Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.ps1') {
    $tokens = $null; $errors = $null
    [Management.Automation.Language.Parser]::ParseFile($file.FullName,[ref]$tokens,[ref]$errors) | Out-Null
    if ($errors.Count) { throw ($errors | Out-String) }
}
$plan = & $installer -ConfigPath $configPath | ConvertFrom-Json
if ($plan.Count -ne 2 -or $plan[0].Task -ne 'MulticaWorker-pilot-remote-Tunnel' -or $plan[1].Task -ne 'MulticaWorker-pilot-remote-Daemon') { throw 'Wrong task identities.' }
foreach ($entry in $plan) {
    if ($entry.Trigger -ne 'AtLogOn' -or $entry.LogonType -ne 'Interactive' -or $entry.StartNow) { throw 'Unsafe default plan.' }
}
$scratch = Join-Path $PSScriptRoot ('test-' + [guid]::NewGuid().ToString('N') + '.json')
try {
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $config.Profile = 'unsafe profile'
    $config | ConvertTo-Json | Set-Content -LiteralPath $scratch
    $rejected = $false
    try { & $installer -ConfigPath $scratch | Out-Null } catch { $rejected = $_.Exception.Message -like '*simple identifier*' }
    if (-not $rejected) { throw 'Invalid profile accepted.' }
} finally { if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch } }
$priorTaskID = $env:MULTICA_TASK_ID
try {
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $config.DeviceName = [Environment]::MachineName
    $config | ConvertTo-Json | Set-Content -LiteralPath $scratch
    $env:MULTICA_TASK_ID = 'fixture-managed-task'
    $rejected = $false
    try { & (Join-Path $PSScriptRoot 'Start-WorkerComponent.ps1') -ConfigPath $scratch -Component Tunnel } catch { $rejected = $_.Exception.Message -like '*managed agent task*' }
    if (-not $rejected) { throw 'Managed-task supervisor guard failed.' }
    foreach ($mode in @('Install','Remove')) {
        $rejected = $false
        try { & $installer -ConfigPath $scratch -Mode $mode | Out-Null } catch { $rejected = $_.Exception.Message -like '*managed agent task*' }
        if (-not $rejected) { throw "Managed-task guard failed for $mode." }
    }
} finally {
    $env:MULTICA_TASK_ID = $priorTaskID
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch }
}
Write-Output 'PASS: syntax, logon-only plan, profile validation, deterministic managed-task guards.'
