# B02 — Domain Primitives

**Phase:** B02
**Status:** Implementation complete; local build/test verification passed.
Two verification items remain environmental: LLVM branch coverage could not be
instrumented on the Windows GNU toolchain, and native libFuzzer could not build
on the same toolchain. Both are recorded as unverified, not as passing claims.
**Date:** 2026-10-19
**Scope:** Exact domain primitives only. No B03 identity/authentication,
reference-data service, ledger, matching engine, wallet, custody, market-data
ingestion, trading, API product, or display configuration was implemented.

---

## 1. Exact B02 scope

The roadmap defines B02 as:

> Exact, safe, well-tested money, identifiers, time, and errors that every
> service shares.

The ten roadmap steps were implemented:

1. `AssetId`, `SymbolId`, `AccountId`, `OrderId`, `TradeId`, `JournalId`, and
   `HoldId`, with parsing, formatting, and serde.
2. Distinct `Price`, `Qty`, `Amount`, `Bps`, and `Multiplier` types with checked
   arithmetic.
3. Strict decimal parsing and canonical formatting with scale validation.
4. Explicit `HalfUp`, `Up`, and `Down` rounding primitives.
5. Exact tick/lot unit conversion and alignment.
6. Unix-nanosecond time internally, ISO-8601/RFC3339 UTC at the edge.
7. Shared error taxonomy and stable public error codes.
8. Instrument, contract, margin-tier, and fee-schedule structs with validation.
9. Property tests, parser regression/fuzz corpus, and overflow tests.
10. Conversion microbenchmarks and crate documentation.

Out of scope and not implemented: B03 identity, B04 reference data, ledger,
matching, margin engine, fees engine, wallet, custody, funding, market data,
API endpoints, persistence, and all later-phase behavior.

---

## 2. Deliverables and design

### 2.1 Identifiers

- `AssetId` and `SymbolId` are opaque `u32` values.
- `OrderId` and `TradeId` are `u64` values.
- `AccountId`, `JournalId`, and `HoldId` are UUIDv7 values. Parsing rejects
  non-v7 UUIDs, including v4 and nil.
- Numeric parsing accepts ASCII digits only. It rejects signs, whitespace,
  decimal points, hex, exponents, and overflows.
- Formatting is canonical. UUID serde is a hyphenated string; numeric IDs use
  their integer representation.

### 2.2 Money and rates

| Type | Representation | Rules |
|------|----------------|-------|
| `Price` | `i64` ticks | Non-negative; checked tick arithmetic |
| `Qty` | `i64` lots | Non-negative; checked lot arithmetic |
| `Amount` | `i128` atomic units | Signed; checked add, subtract, negate |
| `Bps` | `i32` basis points | Checked arithmetic; fee/rate range validation |
| `Multiplier` | positive `i64` numerator/denominator | Exact rational application with explicit rounding |

No arithmetic operator is implemented implicitly for these types. Callers use
named checked methods, which keeps units distinct and makes rounding explicit.
`Amount::from_decimal` and `Amount::to_decimal` use an explicit `Scale`.

### 2.3 Decimal handling

- Maximum scale is 18.
- Syntax is `^-?[0-9]+(\.[0-9]+)?$`.
- Leading `+`, exponents, whitespace, `NaN`, `Infinity`, multiple dots, a
  trailing dot, and a leading dot are rejected.
- More fractional digits than the supplied scale produce
  `DomainError::ScaleExceeded`; values are never silently rounded.
- Output is canonical fixed-scale text, including trailing zeros.
- Negative zero normalizes to zero.
- The complete `i128` range round-trips, including `i128::MIN`.

### 2.4 Rounding and conversion

- `HalfUp`: nearest integer, ties away from zero.
- `Up`: away from zero when a remainder exists.
- `Down`: toward zero.
- `div_round` and `mul_div` use checked `i128` arithmetic and return typed errors
  for zero divisors and overflow.
- `quote_amount` multiplies price ticks, quote atomic units per tick, lots, and
  base atomic units per lot before a single division by `10^base_decimals`.
- `base_atomic` and the alignment helpers require exact lot/tick alignment.

### 2.5 Time

`Timestamp(i64)` stores Unix nanoseconds. It parses RFC3339/ISO-8601 with an
offset, normalizes to UTC, and formats as UTC RFC3339. Millisecond conversion and
all time arithmetic are checked.

### 2.6 Errors

`ErrorCode` contains the architecture's public catalog: `AUTH_*`, `ACC_*`,
`VAL_*`, `MKT_*`, `ORD_*`, `RSK_*`, `WAL_*`, `SYS_*`, and `RATE_*`. Each code
has a stable string, HTTP status, retryability decision, ambiguity flag where
needed, and operator description. `DomainError` carries typed causes and maps
to those codes.

### 2.7 Instrument validation

`Instrument` and `ContractSpec` validate:

- symbol syntax and length;
- distinct base and quote assets;
- positive tick/lot sizes;
- minimum/maximum quantity and notional relationships;
- price-band and fee-rate bounds;
- spot versus perpetual contract shape;
- linear settlement asset matching the quote asset;
- funding interval and cap;
- liquidation fee bounds;
- contiguous margin tiers;
- non-increasing maximum leverage across tiers;
- non-decreasing maintenance-margin rates;
- derived integral `maintenance_amount` continuity at tier boundaries.

`FeeSchedule` validates maker and taker rates, including negative maker rebates.

---

## 3. Files created and modified

### 3.1 Created

Crate implementation:

- `crates/nexora-types/Cargo.toml`
- `crates/nexora-types/README.md`
- `crates/nexora-types/src/lib.rs`
- `crates/nexora-types/src/error.rs`
- `crates/nexora-types/src/id.rs`
- `crates/nexora-types/src/decimal.rs`
- `crates/nexora-types/src/money.rs`
- `crates/nexora-types/src/rounding.rs`
- `crates/nexora-types/src/convert.rs`
- `crates/nexora-types/src/time.rs`
- `crates/nexora-types/src/fee.rs`
- `crates/nexora-types/src/instrument.rs`

Tests and benchmarks:

- `crates/nexora-types/tests/properties.rs`
- `crates/nexora-types/tests/invariants.rs`
- `crates/nexora-types/tests/fuzz_corpus.rs`
- `crates/nexora-types/benches/conversions.rs`

Fuzz scaffold and deterministic corpus:

- `crates/nexora-types/fuzz/Cargo.toml`
- `crates/nexora-types/fuzz/README.md`
- `crates/nexora-types/fuzz/.gitignore`
- `crates/nexora-types/fuzz/fuzz_targets/decimal.rs`
- `crates/nexora-types/fuzz/fuzz_targets/instrument.rs`
- `crates/nexora-types/fuzz/corpus/decimal/*.txt` (12 files)
- `crates/nexora-types/fuzz/corpus/instrument/*.json` (2 files)

Report:

- `reports/B02/REPORT.md`

### 3.2 Modified

- `Cargo.toml`: workspace member, shared dependency versions, financial lints.
- `Cargo.lock`: resolved B02 and test/benchmark dependencies.
- `.github/workflows/ci.yml`: 10-million-operation gate and crate docs step.
- `Makefile`: `domain-gate` and `bench-domain`; `test` runs the invariant gate.
- `scripts/check.ps1`: invariant gate in the local check surface.
- `README.md`: B02 status, crate layout, and commands.

### 3.3 Frontend

No frontend file was created, modified, or deleted. All changes are backend
workspace, Rust crate, test, benchmark, CI, documentation, or report files.

---

## 4. Architecture compliance

| Source | Compliance |
|--------|------------|
| `docs/architecture/03-domain-model.md` | IDs, distinct money types, instrument and contract fields, status enums |
| `docs/architecture/04-modules-and-interfaces.md` | Shared error envelope and public code taxonomy |
| `docs/architecture/09-futures-margin-engine.md` | Linear contract parameters, tier floors/caps, tier leverage, derived maintenance amount |
| `docs/architecture/12-api-rest-and-websocket.md` | Error catalog, HTTP statuses, retryability, decimal-string edge behavior |
| `docs/architecture/15-precision-atomicity-idempotency.md` | Integer representation, prohibited floats/lossy casts, parsing and rounding rules |
| `docs/adr/0025-market-data-display-separation.md` | No display data in the domain crate or financial paths |
| `docs/architecture/20-architecture-decision-records.md` | ADR-003, ADR-011, ADR-012, ADR-014, ADR-016, ADR-023 preserved |

The crate has no I/O, network, database, matching, ledger, or trading logic.
It does not consume display configuration or publish market-data display values.
Serde on money types is an internal integer transport; public REST/WebSocket
DTOs must expose money as decimal strings.

One unit interpretation is explicit: `Instrument::tick_size` is quote atomic
units per tick and `Instrument::lot_size` is base atomic units per lot. This
matches the canonical conversion formula in the precision document and keeps
all conversions exact. B04 must load and validate configuration in those units;
the crate README and field documentation state this requirement.

---

## 5. Verification evidence

Environment: Windows, Rust 1.99.1 `x86_64-pc-windows-gnu`, Go 1.27.2, Node
tooling and contract tools from the approved portable toolchain.

### 5.1 Rust build, lint, and tests

| Check | Result |
|-------|--------|
| `cargo fmt --all -- --check` | Pass |
| `cargo clippy --workspace --all-targets --all-features -- -D warnings` | Pass, 0 warnings |
| `cargo test --workspace --all-features` | Pass |
| `cargo test --release -p nexora-types --test invariants -- --ignored` | Pass |
| `cargo build --workspace --all-targets` | Pass |
| `cargo doc -p nexora-types --no-deps` | Pass |
| `pwsh -File scripts/check.ps1` | Pass, all checks |

The workspace test run executed 68 tests:

- 5 `core-template` tests;
- 8 `nexora-config` tests;
- 43 `nexora-types` unit tests;
- 2 deterministic parser-corpus tests;
- 10 property tests;
- 0 `nexora-telemetry` tests;
- 0 doctests.

The ignored release invariant test executed 10,000,000 randomized transfer
operations and completed in approximately 0.19-0.22 seconds. It checks both
account zero-sum and mirrored journal zero-sum invariants on every operation.

### 5.2 Property and corpus tests

- Ten property tests run 512 generated cases each by default.
- Decimal format-to-parse round trips cover arbitrary `i128` values and every
  scale from 0 through 18.
- Conversion tests compare the implementation against the exact rational
  formula for randomized ticks, lots, sizes, scales, and rounding rules.
- The deterministic corpus covers valid values, negative values, zero padding,
  scale violations, exponent notation, leading plus, whitespace, Unicode,
  multiple dots, empty input, overflow, and valid/invalid instrument JSON.
- The parser corpus test requires every successfully parsed value to survive a
  canonical format-and-reparse cycle.

### 5.3 Microbenchmarks

Command:

```sh
cargo bench -p nexora-types --bench conversions -- \
  --warm-up-time 1 --measurement-time 3 --sample-size 30
```

Observed on this Windows host:

| Benchmark | Estimate | Budget |
|-----------|----------|--------|
| `quote_amount_ticks_lots` | 33.089 ns median (31.386-34.807 ns) | < 50 ns |
| `decimal_parse_amount` | 45.982 ns median | informational |
| `decimal_format_amount` | 342.67 ns median | informational |
| `amount_mul_bps` | 21.239 ns median | informational |

The hot conversion benchmark is below the 50 ns gate on this host. Results are
host-dependent and are not a production SLO.

### 5.4 Other repository checks

| Check | Result |
|-------|--------|
| `gofmt -l libs/go services/hello-service` | No files |
| `go vet` (libs and hello service) | Pass |
| `go test -race` (libs and hello service) | Pass |
| `golangci-lint run` (both modules) | 0 issues |
| `buf lint` and `buf build` | Pass |
| `npm run lint:contracts` | Spectral and AsyncAPI pass |
| `npm run lint:md` | 41 Markdown files, 0 issues |
| `actionlint .github/workflows/ci.yml` | 0 findings |
| `gitleaks detect --redact` | No leaks found |
| `cargo deny check` | Advisories, bans, licenses, sources all pass |

`cargo deny check` reports only non-failing warnings for unused license
allowances and duplicate dependency versions, including duplicates introduced by
test/benchmark dependencies.

### 5.5 Coverage attempt

Installed `llvm-tools-preview` and `cargo-llvm-cov`, then ran:

```sh
cargo llvm-cov -p nexora-types --all-features --summary-only
```

Result: **not measurable on this toolchain**:

```text
error[E0463]: can't find crate for `profiler_builtins`
= note: the compiler may have been built without the profiler runtime
```

The installed `x86_64-pc-windows-gnu` toolchain does not provide the coverage
profiler runtime. No branch-coverage percentage is claimed. The unit,
property, overflow, and corpus suites were written to exercise the reachable
financial-math branches, but the 100% branch-coverage gate remains unverified
until it is run on a host with a supported coverage runtime (for example a
Linux runner or an MSVC toolchain with the profiler runtime).

### 5.6 Fuzz attempt

The stable deterministic corpus and property tests execute successfully. A
native libFuzzer build was attempted with:

```sh
cargo check --manifest-path crates/nexora-types/fuzz/Cargo.toml
```

Result: **not runnable on this host**. `libfuzzer-sys` fails while compiling its
Windows C++ runtime with the MinGW `g++.exe` toolchain. This is a host/toolchain
limitation, not a parser failure. The fuzz targets and seed corpus are present
for execution on a supported libFuzzer host.

---

## 6. Security verification

- Workspace lints deny `clippy::float_arithmetic`, `clippy::float_cmp`,
  `clippy::cast_possible_truncation`, `clippy::cast_sign_loss`, and
  `clippy::cast_possible_wrap` for all Rust code, including `nexora-types`.
- `unsafe_code = "forbid"` remains in force.
- The crate performs no I/O, networking, secret handling, wallet operation, or
  live trading.
- Decimal parsing never uses floating point and rejects malformed or oversized
  input with typed errors.
- Arithmetic returns typed overflow, division-by-zero, alignment, and scale
  errors; the tested paths do not panic, wrap, or silently truncate.
- `gitleaks` found no secrets in the working tree or existing commit history at
  verification time.
- `cargo deny check` passed all four policy classes.
- No credential, private key, seed phrase, or production endpoint was added.

CI-configured but not locally executed: GitHub Actions jobs, Semgrep, Trivy,
Syft, and container scans. They remain unverified until a GitHub runner and
Docker-capable environment are available.

---

## 7. B01 limitations carried forward

1. Docker is not installed. `docker compose up`, image builds, service
   networking, PostgreSQL initialization, Redpanda, Redis, ClickHouse, MinIO,
   and Prometheus runtime checks were not performed.
2. No GitHub remote or runner exists. CI execution, Semgrep, Trivy, Syft, and
   artifact upload remain unverified.
3. GNU Make is not installed. The Makefile was not executed as a whole;
   equivalent commands and `scripts/check.ps1` were run.
4. OTLP trace delivery was not observed end-to-end because no collector was
   running.
5. Ten non-critical npm advisories remain in documentation/contract tooling;
   no critical advisory is known.
6. Branch protection and signed commits remain hosting-platform settings.

---

## 8. New blockers and risks

| Item | Severity | Detail / follow-up |
|------|----------|--------------------|
| Branch coverage unverified | High for acceptance evidence | GNU toolchain lacks `profiler_builtins`; run coverage on a supported Linux/MSVC environment and record the percentage |
| Native libFuzzer unverified | Medium | `libfuzzer-sys` cannot compile its Windows C++ runtime with MinGW here; run on Linux/macOS or a supported Windows toolchain |
| Benchmark is host-specific | Low | The 33 ns result is local; rerun in CI or on production-class hardware before treating it as an SLO |
| Tick/lot unit interpretation | Medium | The crate treats sizes as atomic units; B04 must load configuration in the same units or conversions will be wrong by asset scale |
| Exact integral maintenance amount | Low | Tier boundaries that produce a non-integral maintenance amount are rejected by design; configuration must use compatible boundaries |
| Coverage does not include unreachable defensive branches | Low | Some checked conversions are provably lossless within type bounds; a coverage tool may still report their defensive arms as uncovered |

---

## 9. B02 acceptance-gate status

| Gate | Status | Evidence / limitation |
|------|--------|-----------------------|
| `nexora-types` crate exists with identifiers, money, decimal, rounding, conversion, time, errors, instrument/contract/margin/fee validation | **PASS** | Crate, tests, benchmarks, docs, and workspace wiring delivered |
| 100% branch coverage on financial math | **FAIL (unverified)** | `cargo llvm-cov` cannot run: `profiler_builtins` unavailable on the GNU toolchain. No percentage is claimed |
| All financial types checked; no float arithmetic or lossy casts; CI denies them | **PASS (local equivalent)** | Workspace lints and Clippy pass; CI step is configured but GitHub execution is unverified |
| 10 million randomized operations preserve ledger invariants | **PASS** | Release ignored test passed 10,000,000 transfers, zero-sum invariants checked per operation |
| Parse → format → parse round-trips exactly | **PASS** | Unit tests, scale 0-18 coverage, and 512-case property tests |
| Overflow and scale violations produce typed errors, never panic | **PASS** | Unit and property tests cover scale excess, overflow, division by zero, negative values, and alignment failures |
| Hot conversion benchmark meets `< 50 ns` | **PASS** | `quote_amount_ticks_lots` 33.089 ns median (31.386-34.807 ns) on this host |
| Parser fuzz tests and overflow tests exist | **PASS for corpus/tests; FAIL for native fuzz execution** | 14 corpus files and two fuzz targets exist; deterministic corpus passes; libFuzzer build unavailable here |
| Crate documentation published | **PASS** | `cargo doc -p nexora-types --no-deps` succeeds; crate README and rustdoc are present |
| No frontend files touched | **PASS** | All changed/created paths are backend workspace or reports |
| B03 or later work started | **PASS (not started)** | No identity, auth, reference-data service, ledger, matching, wallet, or trading implementation |

---

## 10. Completion statement

B02 implementation is complete and stopped. All locally executable build, lint,
test, property, corpus, invariant, benchmark, contract, documentation, and
security checks passed. The only non-passing acceptance evidence is the
unmeasured 100% branch-coverage gate, caused by the missing profiler runtime on
this Windows GNU toolchain, and native libFuzzer execution, caused by the
Windows GNU libFuzzer build failure. Both are explicitly unverified rather than
reported as passing.

The next phase is B03 and must not begin without explicit authorization.
