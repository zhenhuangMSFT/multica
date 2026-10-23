param(
    [Parameter(Mandatory)][string]$ConfigPath,
    [Parameter(Mandatory)][ValidateSet('Tunnel','Daemon')][string]$Component
)
$ErrorActionPreference = 'Stop'
# Host operations must never escape a managed task's identity boundary.
if ($env:MULTICA_TASK_ID -or $env:MULTICA_AGENT_ID -or $env:MULTICA_TASK_CONFIG_ROOT) {
    throw 'Run this supervisor from the owner Windows session, not a managed agent task.'
}
$config = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
. (Join-Path $PSScriptRoot 'WorkerPolicy.ps1')
Assert-WorkerTargetHost -Config $config
$logDirectory = Join-Path $PSScriptRoot 'logs'
New-Item -ItemType Directory -Path $logDirectory -Force | Out-Null
$lock = $null
try {
    $lock = [IO.File]::Open((Join-Path $PSScriptRoot "$Component.lock"), 'OpenOrCreate', 'ReadWrite', 'None')
    $logPath = Join-Path $logDirectory "$Component.log"
    function Write-WorkerLog([string]$Message) {
        if ((Test-Path -LiteralPath $logPath) -and (Get-Item -LiteralPath $logPath).Length -gt 5MB) {
            Move-Item -LiteralPath $logPath -Destination "$logPath.1" -Force
        }
        Add-Content -LiteralPath $logPath -Value ("{0:o} {1}" -f [DateTime]::UtcNow,$Message) -Encoding utf8
    }
    # Never terminate or adopt existing processes. Scheduler retries later.
    if ($Component -eq 'Tunnel') {
        if (Get-NetTCPConnection -State Listen -LocalPort $config.ServerPort -ErrorAction SilentlyContinue) {
            throw 'Server port already has a listener; refusing a competing connector.'
        }
        $executable = $config.DevTunnelExe
        $arguments = @('connect', $config.TunnelId)
    } else {
        $profilePattern = '--profile(?:=|\s+)"?' + [regex]::Escape($config.Profile) + '(?:"|\s|$)'
        $existing = Get-CimInstance Win32_Process -Filter "Name='multica.exe'" |
            Where-Object { $_.CommandLine -match 'daemon\s+start' -and $_.CommandLine -match $profilePattern }
        if ($existing) { throw 'The intended daemon is already running; refusing a duplicate.' }
        # This is a readiness gate only; authenticated readiness is an acceptance check.
        $client = [Net.Sockets.TcpClient]::new()
        try {
            $connection = $client.ConnectAsync('127.0.0.1', [int]$config.ServerPort)
            if (-not $connection.Wait(5000) -or -not $client.Connected) { throw 'Tunnel listener is not ready.' }
        } finally { $client.Dispose() }
        $executable = $config.MulticaExe
        $arguments = @(Get-WorkerDaemonArguments -Config $config)
    }
    Write-WorkerLog 'Starting component.'
    & $executable @arguments 2>&1 | ForEach-Object { Write-WorkerLog "$_" }
    Write-WorkerLog "Component exited with code $LASTEXITCODE; scheduler will retry."
    # Even a clean connector exit must restart for a persistent service.
    exit 1
} catch {
    if (Get-Command Write-WorkerLog -ErrorAction SilentlyContinue) { Write-WorkerLog $_.Exception.Message }
    Write-Error $_
    exit 1
} finally {
    if ($lock) { $lock.Dispose() }
}
