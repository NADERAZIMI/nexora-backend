# Contracts

This directory is the source of truth for cross-service contracts.

| Artifact | Tool | Command |
|----------|------|---------|
| Protobuf | `buf` | `buf lint`, `buf generate` |
| OpenAPI 3.1 | Spectral | `npm run lint:openapi` |
| AsyncAPI 3 | AsyncAPI CLI | `npm run lint:asyncapi` |

## Rules

1. Protobuf packages are versioned (`nexora.<domain>.v1`).
2. Breaking changes require a new package version and an ADR.
3. Raw venue market data is append-only and separate from display data.
4. Display contracts in `nexora.display.v1` must never be imported by
   financial modules.
5. Generated code is produced in CI and is not committed.
