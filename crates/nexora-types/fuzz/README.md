# nexora-types fuzz targets

The stable-Rust regression corpus is also executed by `tests/fuzz_corpus.rs`.

LibFuzzer requires a nightly toolchain and `cargo-fuzz`:

```sh
cargo install cargo-fuzz
cargo +nightly fuzz run decimal
cargo +nightly fuzz run instrument
```

The decimal target feeds arbitrary UTF-8 into the strict decimal parser. The
instrument target deserializes arbitrary JSON into `Instrument` and runs
validation. Neither target performs I/O or financial mutation.
