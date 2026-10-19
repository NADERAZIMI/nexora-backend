# nexora-types fuzz targets

The stable-Rust regression corpus is also executed by `tests/fuzz_corpus.rs`.

LibFuzzer requires a nightly toolchain and `cargo-fuzz`:

```sh
cargo install cargo-fuzz --version 0.13.2 --locked
cargo +nightly-2026-10-01 fuzz run decimal
cargo +nightly-2026-10-01 fuzz run instrument
```

The decimal target feeds arbitrary UTF-8 into the strict decimal parser. The
instrument target deserializes arbitrary JSON into `Instrument` and runs
validation. Neither target performs I/O or financial mutation.

## CI smoke verification

`.github/workflows/fuzz.yml` runs the deterministic corpus and a bounded
30-second libFuzzer smoke campaign for each target on Linux. `-max_total_time`
makes the workflow terminate predictably, while `-print_final_stats=1` records
the actual run count and corpus state. A crash fails the job and uploads the
reproducer from `artifacts/`.

## Longer campaigns

Run longer campaigns on a Linux host, or dispatch the workflow manually and
extend the time limit temporarily. Typical local commands are:

```sh
cargo +nightly-2026-10-01 fuzz run decimal -- -max_total_time=3600 -print_final_stats=1
cargo +nightly-2026-10-01 fuzz run instrument -- -max_total_time=3600 -print_final_stats=1
```

Keep the checked-in corpus and add any newly discovered regression input before
closing the campaign.
