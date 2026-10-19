#Requires -Version 7.0
<#
.SYNOPSIS
    Validates protobuf, OpenAPI, and AsyncAPI contracts.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

Push-Location contracts
try {
    buf lint
    if ($LASTEXITCODE -ne 0) { throw 'buf lint failed' }
    buf build
    if ($LASTEXITCODE -ne 0) { throw 'buf build failed' }
    buf generate
    if ($LASTEXITCODE -ne 0) { throw 'buf generate failed' }
} finally {
    Pop-Location
}

npm run lint:openapi
if ($LASTEXITCODE -ne 0) { throw 'OpenAPI lint failed' }
npm run lint:asyncapi
if ($LASTEXITCODE -ne 0) { throw 'AsyncAPI validation failed' }
