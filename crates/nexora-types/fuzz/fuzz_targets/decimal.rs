#![no_main]

use libfuzzer_sys::fuzz_target;
use nexora_types::{Scale, parse_scaled};

fuzz_target!(|data: &[u8]| {
    if let Ok(input) = std::str::from_utf8(data) {
        let scale = Scale::new(6).expect("static scale");
        let _ = parse_scaled(input, scale);
    }
});
