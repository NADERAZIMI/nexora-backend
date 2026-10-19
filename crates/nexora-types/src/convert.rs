//! Centralized exact unit conversion.
//!
//! ```text
//! quote_atomic = price_ticks * tick_size_atomic * qty_lots * lot_size_atomic
//!                / 10^base_decimals
//! ```
//!
//! Tick and lot sizes are integers in the quote and base assets' atomic units,
//! respectively. Multiplication is completed before the single division, and
//! the caller chooses the rounding rule for any remainder. Intermediate and
//! final values are checked; overflow is a typed error, never a wrap or panic.

use crate::decimal::Scale;
use crate::error::DomainError;
use crate::money::{Amount, Price, Qty};
use crate::rounding::{Rounding, div_round};

/// Convert price ticks and lot quantity into quote atomic units.
pub fn quote_amount(
    price: Price,
    qty: Qty,
    tick_size_atomic: i64,
    lot_size_atomic: i64,
    base_decimals: Scale,
    rounding: Rounding,
) -> Result<Amount, DomainError> {
    if tick_size_atomic <= 0 || lot_size_atomic <= 0 {
        return Err(DomainError::InvalidInstrument(
            "tick_size and lot_size must be positive",
        ));
    }

    let numerator = i128::from(price.ticks())
        .checked_mul(i128::from(tick_size_atomic))
        .ok_or(DomainError::Overflow)?
        .checked_mul(i128::from(qty.lots()))
        .ok_or(DomainError::Overflow)?
        .checked_mul(i128::from(lot_size_atomic))
        .ok_or(DomainError::Overflow)?;

    let denominator = base_decimals.factor_i128()?;
    div_round(numerator, denominator, rounding).map(Amount::new)
}

/// Convert lots into base atomic units with checked multiplication.
pub fn base_atomic(qty: Qty, lot_size_atomic: i64) -> Result<i128, DomainError> {
    if lot_size_atomic <= 0 {
        return Err(DomainError::InvalidInstrument("lot_size must be positive"));
    }
    // Both factors are i64, so their exact product always fits i128.
    Ok(i128::from(qty.lots()) * i128::from(lot_size_atomic))
}

#[cfg(test)]
mod tests {
    use super::{base_atomic, quote_amount};
    use crate::decimal::Scale;
    use crate::error::DomainError;
    use crate::money::{Price, Qty};
    use crate::rounding::Rounding;

    #[test]
    fn converts_ticks_and_lots_exactly() {
        let price = Price::new(600_001).unwrap();
        let qty = Qty::new(1_250).unwrap();
        let quote = quote_amount(
            price,
            qty,
            100_000,
            100_000,
            Scale::new(8).unwrap(),
            Rounding::Down,
        )
        .unwrap();
        assert_eq!(quote.atomic(), 75_000_125_000);
        assert_eq!(base_atomic(qty, 100_000).unwrap(), 125_000_000);
    }

    #[test]
    fn applies_explicit_rounding_to_fractional_results() {
        let price = Price::new(1).unwrap();
        let qty = Qty::new(1).unwrap();
        let scale = Scale::new(1).unwrap();
        assert_eq!(
            quote_amount(price, qty, 3, 5, scale, Rounding::Down)
                .unwrap()
                .atomic(),
            1
        );
        assert_eq!(
            quote_amount(price, qty, 3, 5, scale, Rounding::Up)
                .unwrap()
                .atomic(),
            2
        );
        assert_eq!(
            quote_amount(price, qty, 3, 5, scale, Rounding::HalfUp)
                .unwrap()
                .atomic(),
            2
        );
    }

    #[test]
    fn zero_values_are_exact() {
        assert_eq!(
            quote_amount(
                Price::ZERO,
                Qty::new(5).unwrap(),
                1,
                1,
                Scale::ZERO,
                Rounding::Up
            )
            .unwrap(),
            crate::money::Amount::ZERO
        );
        assert_eq!(
            quote_amount(
                Price::new(5).unwrap(),
                Qty::ZERO,
                1,
                1,
                Scale::ZERO,
                Rounding::Up
            )
            .unwrap(),
            crate::money::Amount::ZERO
        );
    }

    #[test]
    fn rejects_invalid_sizes_and_overflow() {
        assert_eq!(
            quote_amount(
                Price::new(1).unwrap(),
                Qty::new(1).unwrap(),
                0,
                1,
                Scale::ZERO,
                Rounding::Down
            ),
            Err(DomainError::InvalidInstrument(
                "tick_size and lot_size must be positive"
            ))
        );
        assert_eq!(
            quote_amount(
                Price::new(1).unwrap(),
                Qty::new(1).unwrap(),
                1,
                0,
                Scale::ZERO,
                Rounding::Down
            ),
            Err(DomainError::InvalidInstrument(
                "tick_size and lot_size must be positive"
            ))
        );
        assert_eq!(
            base_atomic(Qty::new(1).unwrap(), 0),
            Err(DomainError::InvalidInstrument("lot_size must be positive"))
        );
        assert_eq!(
            quote_amount(
                Price::new(i64::MAX).unwrap(),
                Qty::new(i64::MAX).unwrap(),
                i64::MAX,
                i64::MAX,
                Scale::ZERO,
                Rounding::Down
            ),
            Err(DomainError::Overflow)
        );
    }
}
