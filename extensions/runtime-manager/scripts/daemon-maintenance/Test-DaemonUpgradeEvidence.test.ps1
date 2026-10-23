#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Test-DaemonUpgradeEvidence.ps1')
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'upgrade-evidence.example.json') -Raw
$passed = 0

function Assert-True([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw "FAIL: $Label" }
}
function New-Record {
    $r = $template | ConvertFrom-Json -AsHashtable
    $r.sourceCommit = 'a' * 40
    $r.artifactSha256 = 'b' * 64
    $r.rollbackSha256 = 'c' * 64
    $r.evidenceReference = 'issue evidence/build manifest'
    foreach ($key in @($r.Keys)) { if ($r[$key] -is [bool]) { $r[$key] = $true } }
    return $r
}
function Expect-Error([scriptblock]$Body, [string]$Pattern) {
    $errorSeen = $null
    try { & $Body | Out-Null } catch { $errorSeen = $_.Exception.Message }
    Assert-True ($errorSeen -match $Pattern) "expected error: $Pattern, got $errorSeen"
}

$r = $template | ConvertFrom-Json -AsHashtable
Assert-True ((Test-DaemonUpgradeEvidence $r).state -eq 'blocked') 'example must never permit upgrade'
$passed++
foreach ($gate in @('preflight', 'complete')) {
    $r = New-Record
    $r.gate = $gate
    $result = Test-DaemonUpgradeEvidence $r
    Assert-True ($result.state -eq 'record_satisfies_gate') "$gate passes complete record"
    Assert-True (-not $result.authorizesMutation -and -not $result.independentlyVerified) 'no fabricated authorization'
    $passed++
}
foreach ($field in @('rolloutApproved', 'securityScanApproved', 'sourceReviewed', 'targetTestsPassed',
    'externalExecutorAssigned', 'exclusivePathEnforced', 'newAdmissionsBlocked', 'existingRunsDrained',
    'queuedWorkPreserved', 'rollbackReady')) {
    $r = New-Record
    $r[$field] = $false
    Assert-True ((Test-DaemonUpgradeEvidence $r).missingGates -contains $field) "preflight blocks $field"
    $passed++
}
foreach ($field in @('newRevisionObserved', 'identitiesPreserved', 'dependenciesHealthy',
    'canaryPassed', 'contextContinuityVerified', 'admissionsRestored')) {
    $r = New-Record
    $r[$field] = $false
    Assert-True ((Test-DaemonUpgradeEvidence $r).state -eq 'record_satisfies_gate') "$field not required before deployment"
    $r.gate = 'complete'
    Assert-True ((Test-DaemonUpgradeEvidence $r).missingGates -contains $field) "completion requires $field"
    $passed++
}
foreach ($field in @('sourceCommit', 'artifactSha256', 'rollbackSha256')) {
    $r = New-Record
    $r[$field] = 'invalid'
    Assert-True ((Test-DaemonUpgradeEvidence $r).state -eq 'blocked') "invalid $field"
    $passed++
}
$r = New-Record
$r.rollbackSha256 = $r.artifactSha256
Assert-True ((Test-DaemonUpgradeEvidence $r).state -eq 'blocked') 'same hash is not an upgrade'
$passed++
$r = New-Record
$r.rolloutApproved = 'false'
Expect-Error { Test-DaemonUpgradeEvidence $r } 'JSON boolean'
$passed++
$r = New-Record
$r.Remove('exclusivePathEnforced')
Expect-Error { Test-DaemonUpgradeEvidence $r } 'Missing'
$passed++
$r = New-Record
$r.token = 'not-a-secret'
Expect-Error { Test-DaemonUpgradeEvidence $r } 'Unknown'
$passed++
$r = New-Record
$r.gate = 'anything'
Expect-Error { Test-DaemonUpgradeEvidence $r } 'gate must'
$passed++
$pwsh = (Get-Process -Id $PID).Path
$text = & $pwsh -NoProfile -File (Join-Path $PSScriptRoot 'Test-DaemonUpgradeEvidence.ps1') `
    -EvidencePath (Join-Path $PSScriptRoot 'upgrade-evidence.example.json')
Assert-True ($LASTEXITCODE -eq 2) 'CLI exits 2 for blocked example'
Assert-True (($text | ConvertFrom-Json).state -eq 'blocked') 'CLI emits structured blocker'
$passed++
Write-Host "$passed checks passed."
