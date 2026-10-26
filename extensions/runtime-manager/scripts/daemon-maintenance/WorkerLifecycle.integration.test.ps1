#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$WorkerPackagePath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'WorkerLifecycle.psm1') -Force
. (Join-Path $WorkerPackagePath 'WorkerPolicy.ps1')
$bootstrap = Join-Path (Split-Path $PSScriptRoot -Parent) 'devbox'
$mapper = Join-Path $bootstrap 'New-WorkerLogonConfig.ps1'
$manifestTemplate = Get-Content -LiteralPath (Join-Path $bootstrap 'devbox.example.json') -Raw
$workerTemplate = Get-Content -LiteralPath (Join-Path $WorkerPackagePath 'pilot-remote.example.json') -Raw
$scratch = Join-Path $PSScriptRoot ('.integration-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $scratch
$manifestPath = Join-Path $scratch 'bootstrap.json'
$workerPath = Join-Path $scratch 'worker.json'
$now = [DateTimeOffset]::Parse('2026-10-22T07:00:00Z')
try {
    foreach ($endpoint in @('http://localhost:8080', 'http://127.0.0.1:18080')) {
        $manifest = $manifestTemplate | ConvertFrom-Json -AsHashtable
        $worker = $workerTemplate | ConvertFrom-Json
        $manifest.worker.serverUrl = $endpoint
        $manifest.worker.profile = $worker.Profile
        $manifest.worker.computerName = $worker.DeviceName
        $manifest.worker.workspacesRoot = $worker.WorkspacesRoot
        $manifest.worker.maxConcurrentTasks = $worker.MaxConcurrentTasks
        $manifest.worker.tools.multica.path = $worker.MulticaExe
        $manifest | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $manifestPath -Encoding utf8
        $mapped = & $mapper -ConfigPath $manifestPath -UserId $worker.UserId -TunnelId $worker.TunnelId `
            -DevTunnelExe $worker.DevTunnelExe -PowerShellExe $worker.PowerShellExe
        $mapped | Set-Content -LiteralPath $workerPath -Encoding utf8
        $config = $mapped | ConvertFrom-Json
        $plan = @(& (Join-Path $WorkerPackagePath 'Install-WorkerLogon.ps1') -ConfigPath $workerPath -Mode Plan |
            ConvertFrom-Json)
        $launch = @(Get-WorkerDaemonArguments -Config $config)
        if ($plan.Count -ne 2 -or @($plan | Where-Object {
            $_.StartNow -or $_.Trigger -ne 'AtLogOn' -or $_.LogonType -ne 'Interactive'
        }).Count -or $launch -cnotcontains '--no-auto-reload' -or $launch -cnotcontains '--no-auto-update') {
            throw 'Actual worker package violated startup/update policy.'
        }
        $receipt = @{
            schemaVersion = 1; installationId = '77777777-7777-4777-8777-777777777777'
            host = $config.DeviceName; profile = $config.Profile; ownerSid = 'S-1-5-21-1000'; generation = 1
            releaseSha256 = 'a' * 64; configSha256 = (Get-FileHash -LiteralPath $workerPath -Algorithm SHA256).Hash
            tasks = @($plan | ForEach-Object {
                @{ taskPath = '\'; taskName = $_.Task; definitionSha256 = 'b' * 64 }
            })
        }
        $snapshot = @{
            host = $receipt.host; profile = $receipt.profile; ownerSid = $receipt.ownerSid
            releaseSha256 = $receipt.releaseSha256; configSha256 = $receipt.configSha256
            tasks = @(); journalPending = $false
        }
        $reconcile = Get-WorkerTaskReconciliation -Desired $receipt -Snapshot $snapshot
        if ($reconcile.outcome -ne 'blocked' -or $reconcile.reasonCode -ne 'fresh_install_requires_approval') {
            throw 'An offline installer plan must not become trusted ownership or install permission.'
        }
        $expected = @{
            host = $config.DeviceName; profile = $config.Profile
            ownerId = '88888888-8888-4888-8888-888888888888'
            expectedWorkspaceId = $manifest.worker.workspaceId
            endpoint = $manifest.worker.serverUrl
        }
        $observation = $expected.Clone()
        $observation.schemaVersion = 1
        $observation.observationId = '99999999-9999-4999-8999-999999999999'
        $observation.observedAtUtc = $now.ToString("yyyy-MM-dd'T'HH:mm:ss'Z'")
        $observation.state = 'ready'
        $observation.reasonCode = 'ready'
        $observation.apiAuthenticated = $true
        $observation.forwardedEndpointVerified = $true
        $observation.durationMs = 100
        $readiness = Test-WorkerReadinessRecord $observation $expected 0 $now
        if (-not $readiness.dependencyReady -or $readiness.authorizesAdmission -or
            $config.ServerPort -ne ([uri]$expected.endpoint).Port) {
            throw 'Mapped startup/readiness identity or endpoint contract mismatch.'
        }
    }
    Write-Host 'PASS: two mapped v2 configurations through installer Plan, launch policy, ownership refusal and readiness binding.'
} finally {
    foreach ($path in @($manifestPath, $workerPath)) {
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }
    }
    Remove-Item -LiteralPath $scratch
}
