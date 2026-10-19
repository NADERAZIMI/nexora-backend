//! Distinct financial value types with checked arithmetic.
//!
//! `Price` is ticks, `Qty` is lots, `Amount` is signed atomic units, `Bps` is
//! basis points, and `Multiplier` is an exact integer numerator/denominator.
//! The types are deliberately not interchangeable. No arithmetic operator is
//! implemented implicitly; callers use the checked methods and choose an
//! explicit rounding rule where division is involved.
//!
//! Serde on these types is an integer transport for internal typed payloads.
//! Public REST/WebSocket DTOs must expose money as decimal strings, not JSON
//! numbers.

use serde::{Deserialize, Serialize};

use crate::decimal::{Scale, format_scaled, parse_scaled};
use crate::error::DomainError;
use crate::rounding::{Rounding, div_round};

/// Price measured in instrument ticks. Never mixed with raw quote units.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Price(i64);

impl Price {
    pub const ZERO: Self = Self(0);

    /// Create a price from ticks. Prices may be zero only in domain types that
    /// explicitly permit it; instrument validation normally requires positive
    /// prices.
    pub const fn new(ticks: i64) -> Result<Self, DomainError> {
        if ticks < 0 {
            Err(DomainError::Negative)
        } else {
            Ok(Self(ticks))
        }
    }

    pub const fn ticks(self) -> i64 {
        self.0
    }

    pub fn checked_add_ticks(self, delta: i64) -> Result<Self, DomainError> {
        let ticks = self.0.checked_add(delta).ok_or(DomainError::Overflow)?;
        Self::new(ticks)
    }

    pub fn checked_sub_ticks(self, delta: i64) -> Result<Self, DomainError> {
        let ticks = self.0.checked_sub(delta).ok_or(DomainError::Overflow)?;
        Self::new(ticks)
    }

    /// Convert quote atomic units to ticks, requiring exact tick alignment.
    pub fn from_atomic(atomic: i128, tick_size_atomic: i64) -> Result<Self, DomainError> {
        if tick_size_atomic <= 0 {
            return Err(DomainError::InvalidInstrument("tick_size must be positive"));
        }
        if atomic < 0 {
            return Err(DomainError::Negative);
        }
        let divisor = i128::from(tick_size_atomic);
        if atomic % divisor != 0 {
            return Err(DomainError::PriceNotAligned);
        }
        let ticks = i64::try_from(atomic / divisor).map_err(|_| DomainError::Overflow)?;
        Self::new(ticks)
    }

    /// Convert ticks to quote atomic units.
    pub fn to_atomic(self, tick_size_atomic: i64) -> Result<i128, DomainError> {
        if tick_size_atomic <= 0 {
            return Err(DomainError::InvalidInstrument("tick_size must be positive"));
        }
        // Both factors are i64, so their exact product always fits i128.
        Ok(i128::from(self.0) * i128::from(tick_size_atomic))
    }
}

/// Quantity measured in instrument lots. Non-negative; signed position changes
/// use an explicit signed delta instead of a negative `Qty`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Qty(i64);

impl Qty {
    pub const ZERO: Self = Self(0);

    pub const fn new(lots: i64) -> Result<Self, DomainError> {
        if lots < 0 {
            Err(DomainError::Negative)
        } else {
            Ok(Self(lots))
        }
    }

    pub const fn lots(self) -> i64 {
        self.0
    }

    pub fn checked_add(self, other: Self) -> Result<Self, DomainError> {
        let lots = self.0.checked_add(other.0).ok_or(DomainError::Overflow)?;
        Self::new(lots)
    }

    pub fn checked_sub(self, other: Self) -> Result<Self, DomainError> {
        let lots = self.0.checked_sub(other.0).ok_or(DomainError::Overflow)?;
        Self::new(lots)
    }

    /// Convert base atomic units to lots, requiring exact lot alignment.
    pub fn from_atomic(atomic: i128, lot_size_atomic: i64) -> Result<Self, DomainError> {
        if lot_size_atomic <= 0 {
            return Err(DomainError::InvalidInstrument("lot_size must be positive"));
        }
        if atomic < 0 {
            return Err(DomainError::Negative);
        }
        let divisor = i128::from(lot_size_atomic);
        if atomic % divisor != 0 {
            return Err(DomainError::QtyNotAligned);
        }
        let lots = i64::try_from(atomic / divisor).map_err(|_| DomainError::Overflow)?;
        Self::new(lots)
    }

    /// Convert lots to base atomic units.
    pub fn to_atomic(self, lot_size_atomic: i64) -> Result<i128, DomainError> {
        if lot_size_atomic <= 0 {
            return Err(DomainError::InvalidInstrument("lot_size must be positive"));
        }
        // Both factors are i64, so their exact product always fits i128.
        Ok(i128::from(self.0) * i128::from(lot_size_atomic))
    }
}

/// Signed amount in atomic units. Every use site must know the asset scale.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Amount(i128);

impl Amount {
    pub const ZERO: Self = Self(0);

    /// Amounts are signed because journal legs, rebates, and PnL can all be
    /// negative. The asset scale is supplied separately and never inferred.
    pub const fn new(atomic: i128) -> Self {
        Self(atomic)
    }

    pub const fn atomic(self) -> i128 {
        self.0
    }

    pub const fn is_zero(self) -> bool {
        self.0 == 0
    }

    pub const fn is_negative(self) -> bool {
        self.0 < 0
    }

    pub const fn is_positive(self) -> bool {
        self.0 > 0
    }

    pub fn checked_add(self, other: Self) -> Result<Self, DomainError> {
        self.0
            .checked_add(other.0)
            .map(Self)
            .ok_or(DomainError::Overflow)
    }

    pub fn checked_sub(self, other: Self) -> Result<Self, DomainError> {
        self.0
            .checked_sub(other.0)
            .map(Self)
            .ok_or(DomainError::Overflow)
    }

    pub fn checked_neg(self) -> Result<Self, DomainError> {
        self.0.checked_neg().map(Self).ok_or(DomainError::Overflow)
    }

    /// Parse a decimal string with the asset's explicit scale.
    pub fn from_decimal(input: &str, scale: Scale) -> Result<Self, DomainError> {
        parse_scaled(input, scale).map(Self)
    }

    /// Format in canonical fixed-scale form.
    pub fn to_decimal(self, scale: Scale) -> String {
        format_scaled(self.0, scale)
    }

    /// Multiply by a basis-point rate with the operation's explicit rounding.
    pub fn checked_mul_bps(self, bps: Bps, rounding: Rounding) -> Result<Self, DomainError> {
        let product = self
            .0
            .checked_mul(i128::from(bps.get()))
            .ok_or(DomainError::Overflow)?;
        div_round(product, 10_000, rounding).map(Self)
    }
}

/// Signed basis points. One basis point is 0.01%.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Bps(i32);

impl Bps {
    pub const ZERO: Self = Self(0);
    pub const ONE_HUNDRED_PERCENT: i32 = 10_000;

    pub const fn new(bps: i32) -> Self {
        Self(bps)
    }

    pub const fn get(self) -> i32 {
        self.0
    }

    /// Fee and rate values are sane within +/-100%. Configuration validation
    /// still applies narrower limits where appropriate.
    pub const fn is_valid_rate(self) -> bool {
        self.0 >= -Self::ONE_HUNDRED_PERCENT && self.0 <= Self::ONE_HUNDRED_PERCENT
    }

    pub fn checked_add(self, other: Self) -> Result<Self, DomainError> {
        self.0
            .checked_add(other.0)
            .map(Self)
            .ok_or(DomainError::Overflow)
    }

    pub fn checked_sub(self, other: Self) -> Result<Self, DomainError> {
        self.0
            .checked_sub(other.0)
            .map(Self)
            .ok_or(DomainError::Overflow)
    }

    pub fn checked_neg(self) -> Result<Self, DomainError> {
        self.0.checked_neg().map(Self).ok_or(DomainError::Overflow)
    }
}

/// Exact positive rational multiplier, stored as numerator/denominator.
///
/// Used for contract size and other legal scaling factors. Every operation is
/// checked and requires an explicit rounding rule.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct Multiplier {
    numerator: i64,
    denominator: i64,
}

impl Multiplier {
    pub const ONE: Self = Self {
        numerator: 1,
        denominator: 1,
    };

    pub fn new(numerator: i64, denominator: i64) -> Result<Self, DomainError> {
        if numerator <= 0 || denominator <= 0 {
            return Err(DomainError::InvalidMultiplier);
        }
        Ok(Self {
            numerator,
            denominator,
        })
    }

    pub const fn numerator(self) -> i64 {
        self.numerator
    }

    pub const fn denominator(self) -> i64 {
        self.denominator
    }

    /// Apply the multiplier to an amount with explicit rounding.
    pub fn checked_apply(self, amount: Amount, rounding: Rounding) -> Result<Amount, DomainError> {
        let product = i128::from(self.numerator)
            .checked_mul(amount.atomic())
            .ok_or(DomainError::Overflow)?;
        div_round(product, i128::from(self.denominator), rounding).map(Amount::new)
    }
}

#[cfg(test)]
mod tests {
    use super::{Amount, Bps, Multiplier, Price, Qty};
    use crate::decimal::Scale;
    use crate::error::DomainError;
    use crate::rounding::Rounding;

    #[test]
    fn price_and_qty_are_non_negative() {
        assert_eq!(Price::new(-1), Err(DomainError::Negative));
        assert_eq!(Qty::new(-1), Err(DomainError::Negative));
        assert_eq!(Price::new(0).unwrap().ticks(), 0);
        assert_eq!(Qty::new(0).unwrap().lots(), 0);
        assert_eq!(
            Price::new(1).unwrap().checked_sub_ticks(2),
            Err(DomainError::Negative)
        );
        assert_eq!(
            Price::new(1).unwrap().checked_add_ticks(2).unwrap().ticks(),
            3
        );
        assert_eq!(
            Price::new(1).unwrap().checked_sub_ticks(1).unwrap().ticks(),
            0
        );
        assert_eq!(
            Qty::new(1).unwrap().checked_sub(Qty::new(2).unwrap()),
            Err(DomainError::Negative)
        );
        assert_eq!(
            Qty::new(1)
                .unwrap()
                .checked_add(Qty::new(2).unwrap())
                .unwrap()
                .lots(),
            3
        );
        assert_eq!(
            Qty::new(1)
                .unwrap()
                .checked_sub(Qty::new(1).unwrap())
                .unwrap()
                .lots(),
            0
        );
    }

    #[test]
    fn price_and_qty_atomic_alignment() {
        assert_eq!(Price::from_atomic(100_000, 100_000).unwrap().ticks(), 1);
        assert_eq!(Price::from_atomic(200_000, 100_000).unwrap().ticks(), 2);
        assert_eq!(
            Price::from_atomic(199_999, 100_000),
            Err(DomainError::PriceNotAligned)
        );
        assert_eq!(Price::from_atomic(-1, 100_000), Err(DomainError::Negative));
        assert_eq!(
            Price::from_atomic(1, 0),
            Err(DomainError::InvalidInstrument("tick_size must be positive"))
        );
        assert_eq!(Price::new(3).unwrap().to_atomic(100_000).unwrap(), 300_000);
        assert_eq!(
            Price::new(3).unwrap().to_atomic(0),
            Err(DomainError::InvalidInstrument("tick_size must be positive"))
        );
        assert_eq!(
            Price::from_atomic(i128::from(i64::MAX) + 1, 1),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            Price::new(i64::MAX).unwrap().to_atomic(2).unwrap(),
            i128::from(i64::MAX) * 2
        );

        assert_eq!(Qty::from_atomic(100_000, 100_000).unwrap().lots(), 1);
        assert_eq!(
            Qty::from_atomic(100_001, 100_000),
            Err(DomainError::QtyNotAligned)
        );
        assert_eq!(Qty::from_atomic(-1, 100_000), Err(DomainError::Negative));
        assert_eq!(
            Qty::from_atomic(1, 0),
            Err(DomainError::InvalidInstrument("lot_size must be positive"))
        );
        assert_eq!(Qty::new(3).unwrap().to_atomic(100_000).unwrap(), 300_000);
        assert_eq!(
            Qty::new(3).unwrap().to_atomic(0),
            Err(DomainError::InvalidInstrument("lot_size must be positive"))
        );
        assert_eq!(
            Qty::from_atomic(i128::from(i64::MAX) + 1, 1),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            Qty::new(i64::MAX).unwrap().to_atomic(2).unwrap(),
            i128::from(i64::MAX) * 2
        );
    }

    #[test]
    fn amount_checked_arithmetic_and_decimal() {
        let scale = Scale::new(6).unwrap();
        let amount = Amount::from_decimal("30.000050", scale).unwrap();
        assert_eq!(amount.atomic(), 30_000_050);
        assert_eq!(amount.to_decimal(scale), "30.000050");
        assert_eq!(
            amount.checked_add(Amount::new(1)).unwrap().atomic(),
            30_000_051
        );
        assert_eq!(
            amount.checked_sub(Amount::new(1)).unwrap().atomic(),
            30_000_049
        );
        assert_eq!(amount.checked_neg().unwrap().atomic(), -30_000_050);
        assert!(amount.is_positive());
        assert!(amount.checked_neg().unwrap().is_negative());
        assert!(Amount::ZERO.is_zero());
        assert_eq!(
            Amount::new(i128::MAX).checked_add(Amount::new(1)),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            Amount::new(i128::MIN).checked_sub(Amount::new(1)),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            Amount::new(i128::MIN).checked_neg(),
            Err(DomainError::Overflow)
        );
        assert_eq!(Amount::new(0).checked_neg().unwrap(), Amount::ZERO);
    }

    #[test]
    fn bps_and_multiplier_are_checked() {
        assert!(Bps::new(-10_000).is_valid_rate());
        assert!(Bps::new(10_000).is_valid_rate());
        assert!(!Bps::new(10_001).is_valid_rate());
        assert_eq!(Bps::new(5).checked_add(Bps::new(2)).unwrap().get(), 7);
        assert_eq!(Bps::new(5).checked_sub(Bps::new(2)).unwrap().get(), 3);
        assert_eq!(Bps::new(i32::MIN).checked_neg(), Err(DomainError::Overflow));
        assert_eq!(
            Bps::new(i32::MAX).checked_add(Bps::new(1)),
            Err(DomainError::Overflow)
        );
        assert_eq!(
            Bps::new(i32::MIN).checked_sub(Bps::new(1)),
            Err(DomainError::Overflow)
        );

        let multiplier = Multiplier::new(3, 2).unwrap();
        assert_eq!(multiplier.numerator(), 3);
        assert_eq!(multiplier.denominator(), 2);
        assert_eq!(Multiplier::new(0, 1), Err(DomainError::InvalidMultiplier));
        assert_eq!(Multiplier::new(-1, 1), Err(DomainError::InvalidMultiplier));
        assert_eq!(Multiplier::new(1, 0), Err(DomainError::InvalidMultiplier));
        assert_eq!(Multiplier::new(1, -1), Err(DomainError::InvalidMultiplier));
        assert_eq!(
            multiplier
                .checked_apply(Amount::new(5), Rounding::HalfUp)
                .unwrap()
                .atomic(),
            8
        );
        assert_eq!(
            multiplier
                .checked_apply(Amount::new(-5), Rounding::Up)
                .unwrap()
                .atomic(),
            -8
        );
        assert_eq!(
            multiplier.checked_apply(Amount::new(i128::MAX), Rounding::Down),
            Err(DomainError::Overflow)
        );
    }

    #[test]
    fn amount_bps_rounding_is_explicit() {
        let amount = Amount::new(60_000_100_000);
        assert_eq!(
            amount
                .checked_mul_bps(Bps::new(5), Rounding::Up)
                .unwrap()
                .atomic(),
            30_000_050
        );
        assert_eq!(
            Amount::new(1)
                .checked_mul_bps(Bps::new(1), Rounding::Up)
                .unwrap()
                .atomic(),
            1
        );
        assert_eq!(
            Amount::new(1)
                .checked_mul_bps(Bps::new(1), Rounding::Down)
                .unwrap()
                .atomic(),
            0
        );
        assert_eq!(
            Amount::new(i128::MAX).checked_mul_bps(Bps::new(2), Rounding::Down),
            Err(DomainError::Overflow)
        );
    }
}
