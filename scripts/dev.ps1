#Requires -Version 7.0
<#
.SYNOPSIS
    Starts the NEXORA local development stack.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker is required for the local stack. Install Docker Desktop or Docker Engine with Compose v2.'
}

Copy-Item -Path '.env.example' -Destination '.env' -ErrorAction SilentlyContinue

docker compose up -d postgres redpanda redis clickhouse minio otel-collector prometheus
docker compose --profile app up --build -d

Write-Host ''
Write-Host 'NEXORA local stack is running:'
Write-Host '  hello-service  http://localhost:8080/healthz'
Write-Host '  core-template  http://localhost:8081/healthz'
Write-Host '  Prometheus     http://localhost:9090'
Write-Host '  MinIO console  http://localhost:9001'
