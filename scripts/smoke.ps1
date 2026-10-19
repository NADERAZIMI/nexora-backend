#Requires -Version 7.0
<#
.SYNOPSIS
    Smoke-tests the running sample services.
#>
$ErrorActionPreference = 'Stop'

$targets = @(
    @{ Name = 'hello-service'; Base = 'http://localhost:8080' },
    @{ Name = 'core-template'; Base = 'http://localhost:8081' }
)

foreach ($target in $targets) {
    foreach ($path in '/healthz', '/readyz') {
        $response = Invoke-WebRequest -Uri "$($target.Base)$path" -UseBasicParsing
        Write-Host "$($target.Name) $path -> $($response.StatusCode) $($response.Content)"
    }

    $hello = Invoke-WebRequest -Uri "$($target.Base)/v1/hello" -UseBasicParsing
    $requestId = $hello.Headers['X-Request-ID']
    if (-not $requestId) { throw "$($target.Name) did not return X-Request-ID" }
    Write-Host "$($target.Name) /v1/hello -> request id $requestId"

    $metrics = Invoke-WebRequest -Uri "$($target.Base)/metrics" -UseBasicParsing
    if ($metrics.Content -notmatch 'http_requests_total') {
        throw "$($target.Name) metrics missing http_requests_total"
    }
    Write-Host "$($target.Name) /metrics -> ok"
}
