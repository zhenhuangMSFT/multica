[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$metadata = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'extension.json') -Raw | ConvertFrom-Json
if ($metadata.schema_version -ne 1 -or $metadata.name -ne 'runtime-manager' -or
    $metadata.version -ne '0.1.0-dev' -or $metadata.core_contract -ne 'multica-lifecycle/v1' -or
    $metadata.installation -ne 'manual-opt-in' -or $metadata.executor -ne 'unimplemented' -or
    $metadata.complete_drain -ne $false -or $metadata.production_transactions -ne $false) {
    throw 'Unsupported or unsafe runtime-manager package metadata.'
}

$suites = @(
    'scripts\devbox\Invoke-MulticaDevBox.test.ps1'
    'scripts\devbox\New-WorkerLogonConfig.test.ps1'
    'scripts\windows-worker\Test-WorkerLogon.ps1'
    'scripts\windows-worker\Test-WorkerPolicy.ps1'
    'scripts\windows-worker\Test-MapperCompatibility.ps1'
    'scripts\daemon-maintenance\Test-DaemonUpgradeEvidence.test.ps1'
    'scripts\daemon-maintenance\WorkerLifecycle.test.ps1'
    'scripts\daemon-maintenance\WorkerLifecycle.integration.test.ps1'
    'scripts\daemon-maintenance\WorkerSnapshot.test.ps1'
    'scripts\daemon-maintenance\WorkerTransaction.test.ps1'
)
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('multica-runtime-manager-' + [guid]::NewGuid().ToString('N'))
$package = Join-Path $scratch 'runtime-manager'
try {
    New-Item -ItemType Directory -Path $scratch | Out-Null
    Copy-Item -LiteralPath $PSScriptRoot -Destination $package -Recurse
    if (Test-Path -LiteralPath (Join-Path $scratch 'server')) {
        throw 'Standalone fixture unexpectedly contains core source.'
    }
    $powerShell = Join-Path $PSHOME 'pwsh.exe'
    foreach ($suite in $suites) {
        Write-Output "SUITE $suite"
        $arguments = @()
        if ($suite -eq 'scripts\windows-worker\Test-MapperCompatibility.ps1') {
            $arguments = @(
                '-MapperPath', (Join-Path $package 'scripts\devbox\New-WorkerLogonConfig.ps1')
                '-ManifestPath', (Join-Path $package 'scripts\devbox\devbox.example.json')
            )
        }
        if ($suite -eq 'scripts\daemon-maintenance\WorkerLifecycle.integration.test.ps1') {
            $arguments = @('-WorkerPackagePath', (Join-Path $package 'scripts\windows-worker'))
        }
        & $powerShell -NoLogo -NoProfile -NonInteractive -File (Join-Path $package $suite) @arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Standalone suite failed ($LASTEXITCODE): $suite"
        }
    }
    Write-Output "PASS: $($suites.Count) standalone suites; no core source or installed extension required."
}
finally {
    if (Test-Path -LiteralPath $scratch) {
        Remove-Item -LiteralPath $scratch -Recurse -Force
    }
}
