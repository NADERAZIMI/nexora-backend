#![no_main]

use libfuzzer_sys::fuzz_target;
use nexora_types::Instrument;

fuzz_target!(|data: &[u8]| {
    if let Ok(instrument) = serde_json::from_slice::<Instrument>(data) {
        let _ = instrument.validate();
    }
});
