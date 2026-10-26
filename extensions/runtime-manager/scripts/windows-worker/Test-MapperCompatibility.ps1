param(
    [Parameter(Mandatory)][string]$MapperPath,
    [Parameter(Mandatory)][string]$ManifestPath
)
$ErrorActionPreference = 'Stop'
$scratch = Join-Path $PSScriptRoot ('mapper-test-' + [guid]::NewGuid().ToString('N'))
$manifestFile = "$scratch-manifest.json"
$configFile = "$scratch-config.json"
try {
    $manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    foreach ($endpoint in @('http://localhost:8080', 'http://127.0.0.1:18080')) {
        $manifest.worker.serverUrl = $endpoint
        $manifest | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $manifestFile
        $mapped = & $MapperPath -ConfigPath $manifestFile -UserId 'DOMAIN\owner' -TunnelId 'approved-tunnel.region' -DevTunnelExe 'C:\ApprovedTools\devtunnel.exe' -PowerShellExe 'C:\Program Files\PowerShell\7\pwsh.exe'
        $mapped | Set-Content -LiteralPath $configFile
        $config = $mapped | ConvertFrom-Json
        $plan = & (Join-Path $PSScriptRoot 'Install-WorkerLogon.ps1') -ConfigPath $configFile -Mode Plan | ConvertFrom-Json
        if ($plan.Count -ne 2 -or $plan[0].Task -ne "MulticaWorker-$($config.Profile)-Tunnel" -or $plan[1].Task -ne "MulticaWorker-$($config.Profile)-Daemon") { throw 'Mapper/installer task identity mismatch.' }
        if ($config.ServerPort -ne ([uri]$endpoint).Port -or $config.DeviceName -ne $manifest.worker.computerName) { throw 'Mapper lost endpoint/host.' }
    }
    Write-Output 'PASS: two mapper outputs consumed by revised installer Plan; no host operations.'
} finally {
    foreach ($file in @($manifestFile,$configFile)) {
        if (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file }
    }
}
