#Requires -Version 7.0
[CmdletBinding()]
param([string]$EvidencePath)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Test-DaemonUpgradeEvidence {
    param([Parameter(Mandatory)][System.Collections.IDictionary]$Evidence)
    $textFields = @('gate', 'change', 'host', 'profile', 'sourceCommit', 'artifactSha256',
        'rollbackSha256', 'evidenceReference')
    $preflight = @('sourceReviewed', 'targetTestsPassed', 'securityScanApproved', 'rolloutApproved',
        'externalExecutorAssigned', 'exclusivePathEnforced', 'newAdmissionsBlocked',
        'existingRunsDrained', 'queuedWorkPreserved', 'rollbackReady')
    $completion = @('newRevisionObserved', 'identitiesPreserved', 'dependenciesHealthy',
        'canaryPassed', 'contextContinuityVerified', 'admissionsRestored')
    $fields = @('schemaVersion') + $textFields + $preflight + $completion
    foreach ($key in $Evidence.Keys) {
        if ($key -cnotin $fields) { throw "Unknown evidence field: $key." }
    }
    foreach ($key in $fields) {
        if (-not $Evidence.Contains($key)) { throw "Missing evidence field: $key." }
    }
    if ($Evidence.schemaVersion -isnot [long] -and $Evidence.schemaVersion -isnot [int]) {
        throw 'schemaVersion must be integer 1.'
    }
    if ($Evidence.schemaVersion -ne 1) { throw 'Unsupported schemaVersion.' }
    foreach ($key in $textFields) {
        if ($Evidence[$key] -isnot [string] -or [string]::IsNullOrWhiteSpace($Evidence[$key])) {
            throw "$key must be a nonempty string."
        }
    }
    if ($Evidence.gate -cnotin @('preflight', 'complete')) { throw 'gate must be preflight or complete.' }
    foreach ($key in ($preflight + $completion)) {
        if ($Evidence[$key] -isnot [bool]) { throw "$key must be a JSON boolean, not a string or number." }
    }
    $blocked = [Collections.Generic.List[string]]::new()
    if ($Evidence.sourceCommit -cnotmatch '^[0-9a-f]{40}$') { $blocked.Add('sourceCommit: reviewed exact commit required') }
    foreach ($key in @('artifactSha256', 'rollbackSha256')) {
        if ($Evidence[$key] -notmatch '^[0-9a-fA-F]{64}$') { $blocked.Add("${key}: SHA256 required") }
    }
    if ($Evidence.artifactSha256 -ieq $Evidence.rollbackSha256) {
        $blocked.Add('target and rollback hashes must differ for this upgrade')
    }
    if ($Evidence.evidenceReference -match '^REPLACE') { $blocked.Add('evidenceReference: reviewed evidence required') }
    $required = $preflight
    if ($Evidence.gate -ceq 'complete') { $required += $completion }
    foreach ($key in $required) {
        if (-not $Evidence[$key]) { $blocked.Add($key) }
    }
    return [ordered]@{
        gate = $Evidence.gate
        state = $(if ($blocked.Count) { 'blocked' } else { 'record_satisfies_gate' })
        host = $Evidence.host
        profile = $Evidence.profile
        missingGates = @($blocked.ToArray())
        independentlyVerified = $false
        authorizesMutation = $false
    }
}

if ($MyInvocation.InvocationName -ne '.') {
    if (-not $EvidencePath) { throw '-EvidencePath is required.' }
    $record = Get-Content -LiteralPath $EvidencePath -Raw | ConvertFrom-Json -AsHashtable
    $result = Test-DaemonUpgradeEvidence $record
    $result | ConvertTo-Json -Depth 5
    if ($result.state -eq 'blocked') { exit 2 }
}
