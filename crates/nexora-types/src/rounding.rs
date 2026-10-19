//! Explicit rounding primitives.
//!
//! "Up" means away from zero and "Down" means toward zero, so the rule has the
//! same meaning for positive and negative financial values. `HalfUp` rounds a
//! tie away from zero. Callers must choose the rule per operation; there is no
//! implicit default in arithmetic.

use serde::{Deserialize, Serialize};

use crate::error::DomainError;

/// Rounding rule for a division or unit conversion.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Rounding {
    /// Nearest integer; exact halves move away from zero.
    HalfUp,
    /// Away from zero when the remainder is non-zero.
    Up,
    /// Toward zero when the remainder is non-zero.
    Down,
}

impl Rounding {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::HalfUp => "half_up",
            Self::Up => "up",
            Self::Down => "down",
        }
    }
}

/// Exact `numerator / denominator` with an explicit rounding rule.
///
/// On overflow or division by zero a typed error is returned; the function
/// never panics and never wraps.
pub fn div_round(
    numerator: i128,
    denominator: i128,
    rounding: Rounding,
) -> Result<i128, DomainError> {
    if denominator == 0 {
        return Err(DomainError::DivisionByZero);
    }

    let quotient = numerator
        .checked_div(denominator)
        .ok_or(DomainError::Overflow)?;
    let remainder = numerator
        .checked_rem(denominator)
        .ok_or(DomainError::Overflow)?;
    if remainder == 0 {
        return Ok(quotient);
    }

    let adjustment = match rounding {
        Rounding::Down => 0,
        Rounding::Up => away_from_zero(numerator, denominator),
        Rounding::HalfUp => {
            let twice_remainder = remainder
                .unsigned_abs()
                .checked_mul(2)
                .ok_or(DomainError::Overflow)?;
            if twice_remainder >= denominator.unsigned_abs() {
                away_from_zero(numerator, denominator)
            } else {
                0
            }
        }
    };

    quotient
        .checked_add(adjustment)
        .ok_or(DomainError::Overflow)
}

/// Exact `(left * right) / divisor` with checked intermediate arithmetic.
pub fn mul_div(
    left: i128,
    right: i128,
    divisor: i128,
    rounding: Rounding,
) -> Result<i128, DomainError> {
    let product = left.checked_mul(right).ok_or(DomainError::Overflow)?;
    div_round(product, divisor, rounding)
}

fn away_from_zero(numerator: i128, denominator: i128) -> i128 {
    if (numerator < 0) != (denominator < 0) {
        -1
    } else {
        1
    }
}

#[cfg(test)]
mod tests {
    use super::{Rounding, div_round, mul_div};
    use crate::error::DomainError;

    #[test]
    fn rounds_positive_values_explicitly() {
        for (numerator, denominator, down, half_up, up) in [
            (7_i128, 2_i128, 3, 4, 4),
            (5, 2, 2, 3, 3),
            (1, 3, 0, 0, 1),
            (2, 3, 0, 1, 1),
            (4, 2, 2, 2, 2),
        ] {
            assert_eq!(
                div_round(numerator, denominator, Rounding::Down).unwrap(),
                down
            );
            assert_eq!(
                div_round(numerator, denominator, Rounding::HalfUp).unwrap(),
                half_up
            );
            assert_eq!(div_round(numerator, denominator, Rounding::Up).unwrap(), up);
        }
    }

    #[test]
    fn rounds_negative_values_by_magnitude() {
        for (numerator, denominator, down, half_up, up) in [
            (-7_i128, 2_i128, -3, -4, -4),
            (-5, 2, -2, -3, -3),
            (-1, 3, 0, 0, -1),
            (-2, -3, 0, 1, 1),
            (7, -2, -3, -4, -4),
        ] {
            assert_eq!(
                div_round(numerator, denominator, Rounding::Down).unwrap(),
                down
            );
            assert_eq!(
                div_round(numerator, denominator, Rounding::HalfUp).unwrap(),
                half_up
            );
            assert_eq!(div_round(numerator, denominator, Rounding::Up).unwrap(), up);
        }
    }

    #[test]
    fn returns_typed_errors() {
        assert_eq!(
            div_round(1, 0, Rounding::HalfUp),
            Err(DomainError::DivisionByZero)
        );
        assert_eq!(
            div_round(i128::MIN, -1, Rounding::Down),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            mul_div(i128::MAX, 2, 1, Rounding::Down),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            mul_div(1, 1, 0, Rounding::Down),
            Err(DomainError::DivisionByZero)
        );
    }

    #[test]
    fn half_up_tie_threshold_is_exact() {
        assert_eq!(div_round(1, 2, Rounding::HalfUp).unwrap(), 1);
        assert_eq!(div_round(-1, 2, Rounding::HalfUp).unwrap(), -1);
        assert_eq!(div_round(1, 3, Rounding::HalfUp).unwrap(), 0);
        assert_eq!(div_round(2, 3, Rounding::HalfUp).unwrap(), 1);
        assert_eq!(div_round(1, -2, Rounding::HalfUp).unwrap(), -1);
    }

    #[test]
    fn rounding_names_are_stable() {
        assert_eq!(Rounding::HalfUp.as_str(), "half_up");
        assert_eq!(Rounding::Up.as_str(), "up");
        assert_eq!(Rounding::Down.as_str(), "down");
    }
}
