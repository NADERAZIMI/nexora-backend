# nexora-types

Exact domain primitives shared by NEXORA financial cores.

## Purpose

This crate is the single source of truth for identifiers, money units, decimal
parsing, rounding, exact unit conversion, timestamps, public error codes, and
instrument/contract/margin/fee metadata. It is intentionally dependency-light
and contains no I/O, database, network, matching, ledger, or trading logic.

## Financial rules

- Prices are integer ticks. Quantities are integer lots. Amounts are signed
  `i128` atomic units.
- `Price`, `Qty`, and `Amount` are distinct types. Financial code cannot mix
  them by accident.
- No floating-point type is used. Workspace lints deny float arithmetic and
  lossy casts in this crate.
- Rounding is explicit per operation: `HalfUp`, `Up` (away from zero), or
  `Down` (toward zero).
- Decimal parsing rejects exponents, whitespace, a leading `+`, and excess
  fractional digits. It never silently rounds user input.
- Unit conversion multiplies before dividing and uses checked `i128`
  intermediates. Overflow is a typed error, never a panic or wrap.
- Timestamps are Unix nanoseconds internally, UTC, with ISO-8601/RFC3339 at the
  edge.

## Tick and lot units

`Instrument::tick_size` is quote atomic units per tick. `Instrument::lot_size`
is base atomic units per lot. For example, on a 6-decimal quote asset, a 0.10
tick is `100_000`; on an 8-decimal base asset, a 0.001 lot is `100_000`. The
base asset scale is passed explicitly to conversion and alignment helpers.

## Public API boundary

Serde on the money types is an integer transport for internal typed payloads.
Public REST and WebSocket DTOs must expose money as decimal strings, not JSON
numbers. Only the explicit decimal parsing and formatting helpers define the
edge representation.

## Display boundary

This crate has no display, market-data, or NEXORA pricing configuration. Display
adjustments must never feed a financial calculation. Raw market data, canonical
data, and display data remain separate as defined by ADR-025.

## Validation

Domain structs expose a `validate` method. Effective configuration must be
validated before publication to trading paths. Margin-tier maintenance amounts
are checked against the derived continuous formula rather than trusted as
hand-entered values.

## Verification

```sh
cargo test -p nexora-types --all-features
cargo test -p nexora-types --release --test invariants -- --ignored
cargo bench -p nexora-types --bench conversions
cargo doc -p nexora-types --no-deps
```

The release-only invariant test executes ten million randomized ledger-style
amount operations. The benchmark covers hot conversion and decimal paths.

Financial branch coverage is enforced on Linux with `cargo-llvm-cov` and the
nightly toolchain pinned in CI. The financial gate covers `convert.rs`,
`decimal.rs`, `fee.rs`, `instrument.rs`, `money.rs`, and `rounding.rs`:

```sh
cargo +nightly-2026-10-01 llvm-cov --branch -p nexora-types --all-features \
  --ignore-filename-regex='(^|/)(tests|benches|fuzz)/' \
  --json --output-path coverage.json
python3 scripts/check_financial_coverage.py coverage.json
```

Native libFuzzer smoke verification runs on Linux for the decimal and instrument
targets; see `fuzz/README.md` for bounded CI and longer-campaign commands.
