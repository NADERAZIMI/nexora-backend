//! Deterministic regression corpus for the parser fuzz targets.
//!
//! The same inputs are kept under `fuzz/corpus` for `cargo fuzz`. These tests
//! run on stable Rust and fail on a panic or a non-round-tripping valid value.

use std::fs;
use std::path::{Path, PathBuf};

use nexora_types::{Instrument, Scale, format_scaled, parse_scaled};

#[test]
fn decimal_corpus_never_panics_and_round_trips() {
    let scale = Scale::new(6).unwrap();
    for path in corpus_files("decimal", "txt") {
        let input = read_corpus(&path);
        if let Ok(value) = parse_scaled(&input, scale) {
            let canonical = format_scaled(value, scale);
            assert_eq!(
                parse_scaled(&canonical, scale).unwrap(),
                value,
                "corpus input {input:?}"
            );
        }
    }
}

#[test]
fn instrument_corpus_never_panics() {
    for path in corpus_files("instrument", "json") {
        let input = read_corpus(&path);
        if let Ok(instrument) = serde_json::from_str::<Instrument>(&input) {
            let _ = instrument.validate();
        }
    }
}

fn corpus_files(kind: &str, extension: &str) -> Vec<PathBuf> {
    let directory = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("fuzz")
        .join("corpus")
        .join(kind);
    let mut files = fs::read_dir(directory)
        .expect("fuzz corpus directory")
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.extension().and_then(|value| value.to_str()) == Some(extension))
        .collect::<Vec<_>>();
    files.sort();
    assert!(!files.is_empty(), "corpus must not be empty");
    files
}

fn read_corpus(path: &Path) -> String {
    fs::read_to_string(path)
        .unwrap_or_else(|error| panic!("failed to read {}: {error}", path.display()))
        .trim_end_matches(['\r', '\n'])
        .to_owned()
}
