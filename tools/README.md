# Tools

Repository-local developer tooling lives here when it is not part of a service
or shared library.

Current tooling is intentionally thin:

- Contract generation and linting: `contracts/` plus `scripts/contracts.ps1`.
- AsyncAPI validation: `scripts/validate-asyncapi.mjs`.
- Local orchestration: `compose.yaml`, `Makefile`, and `scripts/*.ps1`.

Future additions belong here, not in service directories:

- Schema and fixture generators.
- Replay and determinism harnesses.
- Architecture tests that enforce crate and package boundaries.
- Load-test drivers and fault injectors.

Rules:

1. Tools must be deterministic and runnable in CI.
2. Tools must never contain production credentials or customer data.
3. Financial tools must use the same integer-only primitives as services.
4. A tool that changes production state must require explicit authorization.
