//! Fee schedule primitives.
//!
//! B02 defines the validated rate pair only. Account tiers, volume tiers, and
//! persistence belong to the later reference-data and account phases.

use serde::{Deserialize, Serialize};

use crate::error::DomainError;
use crate::money::Bps;

/// Maker/taker fee rates in basis points.
///
/// Negative maker rates represent rebates. Both rates are constrained to
/// +/-100%, the widest meaningful value; narrower product limits may be applied
/// by the owning configuration service.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct FeeSchedule {
    pub maker_fee_bps: Bps,
    pub taker_fee_bps: Bps,
}

impl FeeSchedule {
    pub const fn new(maker_fee_bps: Bps, taker_fee_bps: Bps) -> Self {
        Self {
            maker_fee_bps,
            taker_fee_bps,
        }
    }

    pub fn validate(&self) -> Result<(), DomainError> {
        if !self.maker_fee_bps.is_valid_rate() {
            return Err(DomainError::InvalidFeeSchedule(
                "maker fee must be within +/-10000 bps",
            ));
        }
        if !self.taker_fee_bps.is_valid_rate() {
            return Err(DomainError::InvalidFeeSchedule(
                "taker fee must be within +/-10000 bps",
            ));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::FeeSchedule;
    use crate::error::DomainError;
    use crate::money::Bps;

    #[test]
    fn accepts_maker_rebates_and_taker_fees() {
        let schedule = FeeSchedule::new(Bps::new(-1), Bps::new(5));
        assert_eq!(schedule.validate(), Ok(()));
        assert_eq!(
            FeeSchedule::new(Bps::new(-10_000), Bps::new(10_000)).validate(),
            Ok(())
        );
    }

    #[test]
    fn rejects_out_of_range_rates() {
        assert_eq!(
            FeeSchedule::new(Bps::new(10_001), Bps::new(5)).validate(),
            Err(DomainError::InvalidFeeSchedule(
                "maker fee must be within +/-10000 bps"
            ))
        );
        assert_eq!(
            FeeSchedule::new(Bps::new(2), Bps::new(-10_001)).validate(),
            Err(DomainError::InvalidFeeSchedule(
                "taker fee must be within +/-10000 bps"
            ))
        );
    }
}
