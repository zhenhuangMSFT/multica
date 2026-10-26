#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $PSScriptRoot 'New-WorkerLogonConfig.ps1'
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'devbox.example.json') -Raw
$scratch = Join-Path $PSScriptRoot ('.logon-test-' + [guid]::NewGuid().ToString('N') + '.json')
$arguments = @{
    ConfigPath = $scratch
    UserId = 'DOMAIN\worker'
    TunnelId = 'approved-tunnel.region'
    DevTunnelExe = 'C:\Program Files\Dev Tunnel\devtunnel.exe'
    PowerShellExe = 'C:\Program Files\PowerShell\7\pwsh.exe'
}
$passed = 0
function Assert-True([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw "FAIL: $Label" }
}
function Write-Manifest($Value) {
    $Value | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $scratch -Encoding utf8
}
function Expect-Error([scriptblock]$Body, [string]$Pattern) {
    $message = $null
    try { & $Body | Out-Null } catch { $message = $_.Exception.Message }
    Assert-True ($message -match $Pattern) "Expected $Pattern, got $message"
}
try {
    foreach ($endpoint in @('http://localhost:8080', 'http://127.0.0.1:18080')) {
        $c = $template | ConvertFrom-Json -AsHashtable
        $c.worker.serverUrl = $endpoint
        Write-Manifest $c
        $result = & $scriptPath @arguments | ConvertFrom-Json -AsHashtable
        Assert-True ($result.Keys.Count -eq 10) 'exact PILOT-18 config shape'
        Assert-True ($result.ServerPort -eq ([uri]$endpoint).Port) 'port mapping'
        $pairs = @{
            Profile = $c.worker.profile
            DeviceName = $c.worker.computerName
            WorkspacesRoot = $c.worker.workspacesRoot
            MulticaExe = $c.worker.tools.multica.path
            MaxConcurrentTasks = $c.worker.maxConcurrentTasks
            UserId = $arguments.UserId
            TunnelId = $arguments.TunnelId
            DevTunnelExe = $arguments.DevTunnelExe
            PowerShellExe = $arguments.PowerShellExe
        }
        foreach ($key in $pairs.Keys) { Assert-True ($result[$key] -ceq $pairs[$key]) "preserve $key" }
        $passed++
    }
    foreach ($endpoint in @('https://api.example.invalid', 'http://localhost:8080/api',
        'http://[::1]:8080', 'https://localhost:8080')) {
        $c.worker.serverUrl = $endpoint
        Write-Manifest $c
        Expect-Error { & $scriptPath @arguments } 'connector startup contract'
        $passed++
    }
    $c.worker.serverUrl = 'http://localhost:8080'
    Write-Manifest $c
    foreach ($bad in @(
        @{ Key = 'TunnelId'; Value = 'bad tunnel'; Pattern = 'TunnelId' },
        @{ Key = 'UserId'; Value = "bad`nuser"; Pattern = 'UserId' },
        @{ Key = 'DevTunnelExe'; Value = 'devtunnel.exe'; Pattern = 'absolute Windows' },
        @{ Key = 'DevTunnelExe'; Value = "C:\bad`npath\devtunnel.exe"; Pattern = 'absolute Windows' },
        @{ Key = 'PowerShellExe'; Value = 'C:\tools\..\pwsh.exe'; Pattern = 'dot segments' }
    )) {
        $copy = $arguments.Clone()
        $copy[$bad.Key] = $bad.Value
        Expect-Error { & $scriptPath @copy } $bad.Pattern
        $passed++
    }
    $c.worker.tools.multica.path = 'C:\tools\multica.cmd'
    Write-Manifest $c
    Expect-Error { & $scriptPath @arguments } 'Multica .exe'
    $passed++
    $c.worker.tools.multica.path = 'C:\tools\multica.exe'
    $c.worker.maxConcurrentTasks = 0
    Write-Manifest $c
    Expect-Error { & $scriptPath @arguments } 'between 1 and 50'
    $passed++
    $c.worker.maxConcurrentTasks = 1
    $c.token = 'not-a-secret'
    Write-Manifest $c
    Expect-Error { & $scriptPath @arguments } 'Unknown field'
    $passed++
    Write-Host "$passed mapping checks passed."
} finally {
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch }
}
