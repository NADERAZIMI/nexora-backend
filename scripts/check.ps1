#Requires -Version 7.0
<#
.SYNOPSIS
    Runs formatting checks, linters, and unit tests for Go and Rust.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$failures = [System.Collections.Generic.List[string]]::new()

function Invoke-Step {
    param([string]$Name, [scriptblock]$Command)
    Write-Host "==> $Name"
    try {
        & $Command
        if ($LASTEXITCODE -ne 0) { throw "exit code $LASTEXITCODE" }
    } catch {
        $failures.Add("$Name : $_")
    }
}

Invoke-Step 'cargo fmt' { cargo fmt --all -- --check }
Invoke-Step 'cargo clippy' { cargo clippy --workspace --all-targets --all-features -- -D warnings }
Invoke-Step 'cargo test' { cargo test --workspace --all-features }
Invoke-Step 'domain invariant gate' { cargo test --release -p nexora-types --test invariants -- --ignored }
Invoke-Step 'gofmt' {
    $unformatted = & gofmt -l libs/go services/hello-service services/identity-service
    if ($unformatted) { throw "gofmt required for: $unformatted" }
}
Invoke-Step 'go vet (libs)' { Push-Location libs/go; try { go vet ./... } finally { Pop-Location } }
Invoke-Step 'go vet (hello)' { Push-Location services/hello-service; try { go vet ./... } finally { Pop-Location } }
Invoke-Step 'go vet (identity)' { Push-Location services/identity-service; try { go vet ./... } finally { Pop-Location } }
Invoke-Step 'go test (libs)' { Push-Location libs/go; try { go test ./... } finally { Pop-Location } }
Invoke-Step 'go test (hello)' { Push-Location services/hello-service; try { go test ./... } finally { Pop-Location } }
Invoke-Step 'go test (identity)' { Push-Location services/identity-service; try { go test ./... } finally { Pop-Location } }
Invoke-Step 'golangci-lint (libs)' { Push-Location libs/go; try { golangci-lint run ./... } finally { Pop-Location } }
Invoke-Step 'golangci-lint (hello)' { Push-Location services/hello-service; try { golangci-lint run ./... } finally { Pop-Location } }
Invoke-Step 'golangci-lint (identity)' { Push-Location services/identity-service; try { golangci-lint run ./... } finally { Pop-Location } }

if ($failures.Count -gt 0) {
    Write-Host ''
    Write-Host 'FAILED:' -ForegroundColor Red
    $failures | ForEach-Object { Write-Host " - $_" -ForegroundColor Red }
    exit 1
}

Write-Host ''
Write-Host 'All checks passed.' -ForegroundColor Green
