# Service Template Guide

Both sample services are reference implementations for later services.

| Language | Template | Use for |
|----------|----------|---------|
| Go | `services/hello-service` | Edge, APIs, identity, wallets, admin |
| Rust | `services/core-template` | Matching, ledger, risk, oracle, funding |

## 1. Required behaviour

Every service must provide:

1. `GET /healthz` — liveness, no dependency checks.
2. `GET /readyz` — readiness, checks required dependencies.
3. `GET /metrics` — Prometheus exposition.
4. JSON structured logs with `service`, `environment`, `version`, and
   `request_id`.
5. OTLP trace export; empty endpoint disables export.
6. Graceful shutdown on SIGTERM/SIGINT with a bounded timeout.
7. Request ID propagation via `X-Request-ID`.
8. Panic recovery that returns a stable error envelope.
9. Configuration validation at startup with fail-fast errors.
10. A container image running as a non-root user.

## 2. Configuration

All variables use the `NEXORA_` prefix:

| Variable | Default | Notes |
|----------|---------|-------|
| `NEXORA_SERVICE_NAME` | template name | Stable telemetry identity |
| `NEXORA_ENVIRONMENT` | `development` | `development`, `test`, `staging`, `production` |
| `NEXORA_HTTP_ADDR` | `:8080` (Go), `0.0.0.0:8081` (Rust) | Listen address |
| `NEXORA_OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4317` | Empty disables traces |
| `NEXORA_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error`, `off` |
| `NEXORA_SHUTDOWN_TIMEOUT_MS` | `10000` | 1000–60000 |

## 3. Creating a new service

1. Copy the relevant template directory.
2. Rename the module/package and binary.
3. Update `go.work` or the Cargo workspace members.
4. Replace the hello endpoint with the approved contract.
5. Add the service to `compose.yaml` under the `app` profile.
6. Add Prometheus scrape configuration.
7. Add unit, integration, and failure tests.
8. Add a runbook and dashboard.
9. Update CODEOWNERS and the phase report.

## 4. Forbidden in a service template

- Business logic in `main`.
- Global mutable state not owned by an actor or explicit store.
- Floating-point arithmetic on financial values.
- Logging request bodies, credentials, or balances.
- Blocking shutdown without a timeout.
- Binding to a public interface by default in development.
