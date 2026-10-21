#Requires -Version 7.0
<#
.SYNOPSIS
    Runs available security scanners.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

function Require-Tool([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        Write-Warning "$Name is not installed; skipping."
        return $false
    }
    return $true
}

if (Require-Tool gitleaks) { gitleaks detect --source . --redact --no-banner }
if (Require-Tool cargo-deny) { cargo deny check }
if (Require-Tool govulncheck) {
    Push-Location libs/go
    try { govulncheck ./... } finally { Pop-Location }
    Push-Location services/hello-service
    try { govulncheck ./... } finally { Pop-Location }
    Push-Location services/identity-service
    try { govulncheck ./... } finally { Pop-Location }
}
if (Require-Tool trivy) {
    trivy fs --scanners vuln,secret,misconfig --exit-code 1 .
}
if (Require-Tool syft) {
    syft dir:. -o spdx-json=reports/B01/sbom.spdx.json
}
