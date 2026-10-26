# Pure policy helpers; loading this file does not administer the host.
function Assert-WorkerTargetHost {
    param([Parameter(Mandatory)]$Config)
    $target = [string]$Config.DeviceName
    if ([string]::IsNullOrWhiteSpace($target) -or
        -not [string]::Equals($target, [Environment]::MachineName, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Target host mismatch: configured DeviceName must match the actual Windows computer name."
    }
}

function Get-WorkerDaemonArguments {
    param([Parameter(Mandatory)]$Config)
    @(
        'daemon', 'start', '--foreground', '--profile', $Config.Profile,
        '--device-name', $Config.DeviceName, '--max-concurrent-tasks', "$($Config.MaxConcurrentTasks)",
        '--workspaces-root', $Config.WorkspacesRoot, '--no-auto-update', '--no-auto-reload'
    )
}
