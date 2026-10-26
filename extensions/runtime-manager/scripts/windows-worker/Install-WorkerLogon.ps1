[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)][string]$ConfigPath,
    [ValidateSet('Plan','Install','Remove')][string]$Mode = 'Plan'
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'WorkerPolicy.ps1')
$config = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
foreach ($name in @('UserId','Profile','TunnelId','DeviceName','MulticaExe','DevTunnelExe','PowerShellExe','WorkspacesRoot')) {
    if (-not $config.$name -or "$($config.$name)" -match '["\r\n]') { throw "Missing or invalid configuration: $name" }
}
if ($config.Profile -notmatch '^[a-zA-Z0-9_-]+$') { throw 'Profile must be a simple identifier.' }
if ($config.TunnelId -notmatch '^[a-zA-Z0-9][a-zA-Z0-9.-]*$') { throw 'Invalid tunnel identifier.' }
foreach ($name in @('MulticaExe','DevTunnelExe','PowerShellExe','WorkspacesRoot')) {
    if (-not [IO.Path]::IsPathFullyQualified($config.$name)) { throw "Path must be absolute: $name" }
}
if ($config.ServerPort -lt 1 -or $config.ServerPort -gt 65535) { throw 'Invalid server port.' }
if ($config.MaxConcurrentTasks -lt 1) { throw 'Invalid concurrency.' }
$taskPrefix = "MulticaWorker-$($config.Profile)"
$plan = @('Tunnel','Daemon') | ForEach-Object {
    [pscustomobject]@{ Task="$taskPrefix-$_"; Component=$_; User=$config.UserId; Trigger='AtLogOn'; LogonType='Interactive'; Restart='1 minute, up to 999 retries'; StartNow=$false }
}
if ($Mode -eq 'Plan') { $plan | ConvertTo-Json; return }
Assert-WorkerTargetHost -Config $config
if ($env:MULTICA_TASK_ID -or $env:MULTICA_AGENT_ID -or $env:MULTICA_TASK_CONFIG_ROOT) {
    throw 'Host startup administration is unavailable in a managed agent task. Use an owner terminal.'
}
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$targetSid = ([Security.Principal.NTAccount]::new($config.UserId)).Translate([Security.Principal.SecurityIdentifier]).Value
if ($identity.User.Value -ne $targetSid) { throw 'Run as the configured owner; credentials are never copied.' }
if (-not ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run the owner terminal as administrator.'
}
$lock = [IO.File]::Open((Join-Path $PSScriptRoot 'installation.lock'), 'OpenOrCreate','ReadWrite','None')
try {
    $names = @($plan.Task)
    $existing = @(Get-ScheduledTask -TaskPath '\' -ErrorAction Stop | Where-Object TaskName -In $names)
    if ($Mode -eq 'Remove') {
        foreach ($task in $existing) {
            if ($task.State -eq 'Running') { throw 'Task is running; drain it in the approved maintenance window first.' }
            if ($task.Description -ne 'Multica worker logon supervisor v1') { throw 'Refusing to remove an unowned task.' }
        }
        foreach ($task in $existing) {
            if ($PSCmdlet.ShouldProcess($task.TaskName,'Remove logon task')) { Unregister-ScheduledTask -TaskPath '\' -TaskName $task.TaskName -Confirm:$false }
        }
        return
    }
    if ($existing.Count) { throw 'Tasks already exist. Inspect them; no existing registration will be overwritten.' }
    foreach ($path in @($config.MulticaExe,$config.DevTunnelExe,$config.PowerShellExe)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Executable missing: $path" }
    }
    if (-not (Test-Path -LiteralPath $config.WorkspacesRoot -PathType Container)) { throw 'Existing workspace root is missing.' }
    if (-not $PSCmdlet.ShouldProcess($taskPrefix,'Register two logon tasks without starting them')) { return }
    $resolvedConfig = (Resolve-Path -LiteralPath $ConfigPath).Path
    $runner = Join-Path $PSScriptRoot 'Start-WorkerComponent.ps1'
    $created = @()
    try {
        foreach ($entry in $plan) {
            $arguments = '-NoProfile -NonInteractive -WindowStyle Hidden -File "{0}" -ConfigPath "{1}" -Component {2}' -f $runner,$resolvedConfig,$entry.Component
            $action = New-ScheduledTaskAction -Execute $config.PowerShellExe -Argument $arguments -WorkingDirectory $PSScriptRoot
            $trigger = New-ScheduledTaskTrigger -AtLogOn -User $config.UserId
            $principal = New-ScheduledTaskPrincipal -UserId $config.UserId -LogonType Interactive -RunLevel Limited
            $settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
            Register-ScheduledTask -TaskPath '\' -TaskName $entry.Task -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Description 'Multica worker logon supervisor v1' | Out-Null
            $created += $entry.Task
        }
    } catch {
        foreach ($name in $created) { Unregister-ScheduledTask -TaskPath '\' -TaskName $name -Confirm:$false }
        throw
    }
    $names | ForEach-Object { Get-ScheduledTask -TaskPath '\' -TaskName $_ -ErrorAction Stop } | Select-Object TaskName,State
} finally { $lock.Dispose() }
