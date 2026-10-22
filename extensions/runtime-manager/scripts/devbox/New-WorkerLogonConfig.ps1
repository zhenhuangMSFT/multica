#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ConfigPath,
    [Parameter(Mandatory)][string]$UserId,
    [Parameter(Mandatory)][string]$TunnelId,
    [Parameter(Mandatory)][string]$DevTunnelExe,
    [Parameter(Mandatory)][string]$PowerShellExe
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Invoke-MulticaDevBox.ps1') -ConfigPath $ConfigPath

$config = Read-DevBoxConfig $ConfigPath
$worker = $config.worker
$endpoint = [uri]$worker.serverUrl
if ($endpoint.Scheme -cne 'http' -or $endpoint.Host -notin @('localhost', '127.0.0.1') -or
    $endpoint.AbsolutePath -cne '/' -or $endpoint.Port -lt 1 -or $endpoint.Port -gt 65535) {
    throw 'The connector startup contract requires an HTTP localhost or 127.0.0.1 server root endpoint with a valid port.'
}
if ([string]::IsNullOrWhiteSpace($UserId) -or $UserId -match '["\r\n]' -or $UserId -ne $UserId.Trim()) {
    throw 'UserId must be the intended Windows owner identity, without quotes, surrounding whitespace or newlines.'
}
if ($TunnelId -notmatch '^[A-Za-z0-9][A-Za-z0-9.-]*$') { throw 'Invalid TunnelId.' }
foreach ($entry in @{ DevTunnelExe = $DevTunnelExe; PowerShellExe = $PowerShellExe }.GetEnumerator()) {
    if ($entry.Value -notmatch '^[A-Za-z]:\\[^<>:"|?*\r\n]+\.exe$' -or
        $entry.Value -match '(^|\\)\.\.?($|\\)') {
        throw "$($entry.Key) must be an absolute Windows executable path without dot segments."
    }
}
if ($worker.tools.multica.path -notmatch '\.exe$') {
    throw 'Worker startup requires a pinned Multica .exe path.'
}

[ordered]@{
    UserId = $UserId
    Profile = $worker.profile
    TunnelId = $TunnelId
    DeviceName = $worker.computerName
    ServerPort = $endpoint.Port
    MaxConcurrentTasks = $worker.maxConcurrentTasks
    WorkspacesRoot = $worker.workspacesRoot
    MulticaExe = $worker.tools.multica.path
    DevTunnelExe = $DevTunnelExe
    PowerShellExe = $PowerShellExe
} | ConvertTo-Json
