//! Strict decimal parsing and canonical formatting.
//!
//! Financial integers are always interpreted with an explicit scale. Parsing
//! accepts only `^-?[0-9]+(\.[0-9]+)?$`; it rejects exponents, `NaN`,
//! `Infinity`, a leading `+`, whitespace, and excess fractional digits rather
//! than silently rounding user input.

use std::fmt;

use serde::{Deserialize, Serialize};

use crate::error::DomainError;

/// Maximum supported asset scale (18 decimal places, as used by 18-decimal
/// tokens). The limit keeps `10^scale` exact in an `i128`.
pub const MAX_SCALE: u8 = 18;

/// A validated decimal scale in the range `0..=18`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Scale(u8);

impl Scale {
    /// Create a scale after validating the architecture limit.
    pub const fn new(scale: u8) -> Result<Self, DomainError> {
        if scale <= MAX_SCALE {
            Ok(Self(scale))
        } else {
            Err(DomainError::InvalidScale(scale))
        }
    }

    /// The scale used by whole-unit values.
    pub const ZERO: Self = Self(0);

    /// Raw decimal places.
    pub const fn get(self) -> u8 {
        self.0
    }

    /// Exact `10^scale` factor. The constructor guarantees this fits `u128`.
    pub const fn factor(self) -> u128 {
        let mut factor = 1_u128;
        let mut index = 0_u8;
        while index < self.0 {
            factor *= 10;
            index += 1;
        }
        factor
    }

    /// Exact `10^scale` factor as an `i128`.
    pub fn factor_i128(self) -> Result<i128, DomainError> {
        i128::try_from(self.factor()).map_err(|_| DomainError::Overflow)
    }
}

impl Default for Scale {
    fn default() -> Self {
        Self::ZERO
    }
}

impl fmt::Display for Scale {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(formatter, "{}", self.0)
    }
}

/// Parse a decimal string into an integer scaled by `scale`.
///
/// The result is exact. Values with more fractional digits than `scale` are
/// rejected; values with fewer are zero-padded. Negative zero normalizes to
/// zero. The full `i128` range is supported.
pub fn parse_scaled(input: &str, scale: Scale) -> Result<i128, DomainError> {
    let bytes = input.as_bytes();
    if bytes.is_empty() {
        return Err(DomainError::InvalidDecimal);
    }

    let negative = bytes[0] == b'-';
    let digits = if negative { &bytes[1..] } else { bytes };
    if digits.is_empty() {
        return Err(DomainError::InvalidDecimal);
    }

    let mut magnitude = 0_u128;
    let mut fractional_digits = 0_u8;
    let mut seen_digit = false;
    let mut seen_dot = false;
    let mut ended_with_dot = false;

    for byte in digits {
        match *byte {
            b'0'..=b'9' => {
                let digit = u128::from(*byte - b'0');
                magnitude = magnitude
                    .checked_mul(10)
                    .and_then(|value| value.checked_add(digit))
                    .ok_or(DomainError::Overflow)?;
                seen_digit = true;
                ended_with_dot = false;
                if seen_dot {
                    // The maximum scale is 18, so the counter cannot overflow
                    // before the scale check below rejects the input.
                    fractional_digits += 1;
                    if fractional_digits > scale.get() {
                        return Err(DomainError::ScaleExceeded {
                            max: scale.get(),
                            actual: fractional_digits,
                        });
                    }
                }
            }
            b'.' if !seen_dot => {
                if !seen_digit {
                    return Err(DomainError::InvalidDecimal);
                }
                seen_dot = true;
                ended_with_dot = true;
            }
            _ => return Err(DomainError::InvalidDecimal),
        }
    }

    if ended_with_dot {
        return Err(DomainError::InvalidDecimal);
    }

    if fractional_digits < scale.get() {
        let missing = scale.get() - fractional_digits;
        magnitude = magnitude
            .checked_mul(pow10(missing))
            .ok_or(DomainError::Overflow)?;
    }

    if negative {
        if magnitude > (1_u128 << 127) {
            return Err(DomainError::Overflow);
        }
        if magnitude == (1_u128 << 127) {
            return Ok(i128::MIN);
        }
        let value = i128::try_from(magnitude).map_err(|_| DomainError::Overflow)?;
        Ok(-value)
    } else {
        if magnitude > (1_u128 << 127) - 1 {
            return Err(DomainError::Overflow);
        }
        i128::try_from(magnitude).map_err(|_| DomainError::Overflow)
    }
}

/// Format a scaled integer in canonical fixed-scale form.
///
/// The output always contains exactly `scale` fractional digits. `-0` is never
/// emitted. Examples at scale 6: `30_000_050` becomes `30.000050`, and `-1`
/// becomes `-0.000001`.
pub fn format_scaled(value: i128, scale: Scale) -> String {
    let negative = value < 0;
    let magnitude = value.unsigned_abs();
    let factor = scale.factor();
    let whole = magnitude / factor;
    let fraction = magnitude % factor;
    let sign = if negative { "-" } else { "" };

    if scale == Scale::ZERO {
        format!("{sign}{whole}")
    } else {
        let width = usize::from(scale.get());
        format!("{sign}{whole}.{fraction:0width$}")
    }
}

fn pow10(exponent: u8) -> u128 {
    let mut value = 1_u128;
    let mut index = 0_u8;
    while index < exponent {
        value *= 10;
        index += 1;
    }
    value
}

#[cfg(test)]
mod tests {
    use super::{MAX_SCALE, Scale, format_scaled, parse_scaled};
    use crate::error::DomainError;

    fn scale(value: u8) -> Scale {
        Scale::new(value).expect("valid test scale")
    }

    #[test]
    fn scale_validation_and_factor() {
        assert_eq!(Scale::new(0).unwrap(), Scale::ZERO);
        assert_eq!(
            Scale::new(MAX_SCALE).unwrap().factor(),
            1_000_000_000_000_000_000
        );
        assert_eq!(
            Scale::new(MAX_SCALE + 1),
            Err(DomainError::InvalidScale(MAX_SCALE + 1))
        );
        assert_eq!(format!("{}", scale(6)), "6");
        assert_eq!(Scale::default(), Scale::ZERO);
        assert_eq!(scale(6).factor_i128().unwrap(), 1_000_000);
    }

    #[test]
    fn parses_canonical_and_zero_padded_values() {
        assert_eq!(parse_scaled("30.00005", scale(6)).unwrap(), 30_000_050);
        assert_eq!(parse_scaled("30.000050", scale(6)).unwrap(), 30_000_050);
        assert_eq!(parse_scaled("00030.00005", scale(6)).unwrap(), 30_000_050);
        assert_eq!(parse_scaled("0", scale(6)).unwrap(), 0);
        assert_eq!(parse_scaled("-0.000000", scale(6)).unwrap(), 0);
        assert_eq!(parse_scaled("-1.25", scale(2)).unwrap(), -125);
        assert_eq!(parse_scaled("1", scale(0)).unwrap(), 1);
    }

    #[test]
    fn rejects_non_canonical_and_unsafe_inputs() {
        for input in [
            "", "-", "+1", " 1", "1 ", "1.", ".1", "1..2", "1.2.3", "1e6", "NaN", "∞",
        ] {
            assert_eq!(
                parse_scaled(input, scale(6)),
                Err(DomainError::InvalidDecimal),
                "input {input:?}"
            );
        }
        assert_eq!(
            parse_scaled("1.1234567", scale(6)),
            Err(DomainError::ScaleExceeded { max: 6, actual: 7 })
        );
        assert_eq!(
            parse_scaled("999999999999999999999999999999999999999", scale(0)),
            Err(DomainError::Overflow)
        );
    }

    #[test]
    fn supports_entire_i128_range() {
        assert_eq!(
            parse_scaled(&i128::MAX.to_string(), scale(0)).unwrap(),
            i128::MAX
        );
        assert_eq!(
            parse_scaled(&i128::MIN.to_string(), scale(0)).unwrap(),
            i128::MIN
        );
        assert_eq!(format_scaled(i128::MAX, scale(0)), i128::MAX.to_string());
        assert_eq!(format_scaled(i128::MIN, scale(0)), i128::MIN.to_string());
        assert_eq!(
            parse_scaled(&format_scaled(i128::MIN, scale(18)), scale(18)).unwrap(),
            i128::MIN
        );
    }

    #[test]
    fn formats_fixed_scale_and_normalizes_negative_zero() {
        assert_eq!(format_scaled(30_000_050, scale(6)), "30.000050");
        assert_eq!(format_scaled(-1, scale(6)), "-0.000001");
        assert_eq!(format_scaled(0, scale(6)), "0.000000");
        assert_eq!(format_scaled(0, scale(0)), "0");
        assert_eq!(format_scaled(5, scale(0)), "5");
        assert_eq!(format_scaled(-5, scale(0)), "-5");
    }

    #[test]
    fn round_trips_every_scale() {
        for scale_value in 0..=MAX_SCALE {
            let scale = scale(scale_value);
            for value in [
                0,
                1,
                -1,
                10,
                -10,
                1_234_567_890,
                -1_234_567_890,
                i128::from(i64::MAX),
                i128::from(i64::MIN),
            ] {
                let text = format_scaled(value, scale);
                assert_eq!(parse_scaled(&text, scale).unwrap(), value);
            }
        }
    }
}
