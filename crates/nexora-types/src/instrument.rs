//! Instrument, contract, margin-tier, and fee schedule validation.
//!
//! Tick and lot sizes are integers in atomic units: `tick_size` is quote atomic
//! units per tick and `lot_size` is base atomic units per lot. A conversion
//! therefore needs the base asset scale; quote decimals are already encoded in
//! `tick_size`. This keeps every conversion exact and avoids hidden units.

use serde::{Deserialize, Serialize};

use crate::convert::{self, base_atomic};
use crate::decimal::{Scale, format_scaled, parse_scaled};
use crate::error::DomainError;
use crate::id::{AssetId, SymbolId};
use crate::money::{Amount, Bps, Multiplier, Price, Qty};
use crate::rounding::Rounding;

/// Product kind.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum InstrumentKind {
    Spot,
    Perpetual,
}

/// Trading state of an instrument.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum InstrumentStatus {
    Active,
    ReduceOnly,
    Halted,
    Delisted,
}

/// One bracket in a tiered maintenance-margin schedule.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct MarginTier {
    pub notional_floor: Amount,
    pub notional_cap: Amount,
    pub max_leverage: u16,
    pub mmr_bps: Bps,
    /// Derived deduction that makes maintenance margin continuous at the
    /// preceding bracket boundary. The first bracket must use zero.
    pub maintenance_amount: Amount,
}

impl MarginTier {
    pub fn validate(&self) -> Result<(), DomainError> {
        if self.notional_floor.is_negative() || self.notional_cap.is_negative() {
            return Err(DomainError::InvalidMarginTier(
                "notional bounds must be non-negative",
            ));
        }
        if self.notional_cap <= self.notional_floor {
            return Err(DomainError::InvalidMarginTier(
                "notional cap must exceed floor",
            ));
        }
        if self.max_leverage == 0 {
            return Err(DomainError::InvalidMarginTier(
                "max leverage must be positive",
            ));
        }
        if self.mmr_bps.get() < 0 || !self.mmr_bps.is_valid_rate() {
            return Err(DomainError::InvalidMarginTier(
                "maintenance margin rate must be between 0 and 10000 bps",
            ));
        }
        if self.maintenance_amount.is_negative() {
            return Err(DomainError::InvalidMarginTier(
                "maintenance amount must be non-negative",
            ));
        }
        Ok(())
    }
}

/// Linear USDT-M contract parameters.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ContractSpec {
    pub contract_size: Multiplier,
    pub settlement_asset: AssetId,
    pub max_leverage: u16,
    pub margin_tiers: Vec<MarginTier>,
    pub funding_interval_secs: u32,
    pub funding_cap_bps: i32,
    pub liquidation_fee_bps: Bps,
    pub adl_enabled: bool,
}

impl ContractSpec {
    pub fn validate(&self) -> Result<(), DomainError> {
        if self.max_leverage == 0 {
            return Err(DomainError::InvalidContract(
                "max leverage must be positive",
            ));
        }
        if self.margin_tiers.is_empty() {
            return Err(DomainError::InvalidContract(
                "at least one margin tier is required",
            ));
        }
        if self.funding_interval_secs == 0 || self.funding_interval_secs > 604_800 {
            return Err(DomainError::InvalidContract(
                "funding interval must be between 1 second and 7 days",
            ));
        }
        if self.funding_cap_bps < 0 || self.funding_cap_bps > 10_000 {
            return Err(DomainError::InvalidContract(
                "funding cap must be between 0 and 10000 bps",
            ));
        }
        if self.liquidation_fee_bps.get() < 0 || !self.liquidation_fee_bps.is_valid_rate() {
            return Err(DomainError::InvalidContract(
                "liquidation fee must be between 0 and 10000 bps",
            ));
        }

        let mut previous: Option<&MarginTier> = None;
        for tier in &self.margin_tiers {
            tier.validate()?;
            if tier.max_leverage > self.max_leverage {
                return Err(DomainError::InvalidContract(
                    "tier leverage exceeds contract maximum",
                ));
            }

            match previous {
                None => {
                    if !tier.notional_floor.is_zero() {
                        return Err(DomainError::InvalidContract(
                            "first margin tier must start at zero",
                        ));
                    }
                    if !tier.maintenance_amount.is_zero() {
                        return Err(DomainError::InvalidContract(
                            "first maintenance amount must be zero",
                        ));
                    }
                }
                Some(prior) => {
                    if tier.notional_floor != prior.notional_cap {
                        return Err(DomainError::InvalidContract(
                            "margin tiers must be contiguous",
                        ));
                    }
                    if tier.max_leverage > prior.max_leverage {
                        return Err(DomainError::InvalidContract(
                            "maximum leverage must not increase across tiers",
                        ));
                    }
                    if tier.mmr_bps.get() < prior.mmr_bps.get() {
                        return Err(DomainError::InvalidContract(
                            "maintenance margin rate must not decrease",
                        ));
                    }

                    // Both validated rates are in 0..=10_000, so their
                    // difference cannot overflow i32.
                    let rate_delta = tier.mmr_bps.get() - prior.mmr_bps.get();
                    let product = tier
                        .notional_floor
                        .atomic()
                        .checked_mul(i128::from(rate_delta))
                        .ok_or(DomainError::Overflow)?;
                    if product % 10_000 != 0 {
                        return Err(DomainError::InvalidContract(
                            "maintenance amount must be integral",
                        ));
                    }
                    let expected = prior
                        .maintenance_amount
                        .checked_add(Amount::new(product / 10_000))?;
                    if tier.maintenance_amount != expected {
                        return Err(DomainError::InvalidContract(
                            "maintenance amount must be derived from the prior tier",
                        ));
                    }
                }
            }
            previous = Some(tier);
        }
        Ok(())
    }
}

/// Validated instrument metadata. Construct the struct, then call
/// [`Instrument::validate`] before publishing it to a trading path.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Instrument {
    pub symbol_id: SymbolId,
    pub symbol: String,
    pub kind: InstrumentKind,
    pub base: AssetId,
    pub quote: AssetId,
    pub tick_size: i64,
    pub lot_size: i64,
    pub min_qty: Qty,
    pub max_qty: Qty,
    pub min_notional: Amount,
    pub max_notional: Amount,
    pub price_band_bps: i32,
    pub maker_fee_bps: Bps,
    pub taker_fee_bps: Bps,
    pub contract: Option<ContractSpec>,
    pub status: InstrumentStatus,
}

impl Instrument {
    pub fn validate(&self) -> Result<(), DomainError> {
        if self.symbol.is_empty() || self.symbol.len() > 32 {
            return Err(DomainError::InvalidInstrument(
                "symbol length must be between 1 and 32 bytes",
            ));
        }
        if !self.symbol.as_bytes().iter().all(|byte| {
            byte.is_ascii_uppercase() || byte.is_ascii_digit() || *byte == b'-' || *byte == b'_'
        }) {
            return Err(DomainError::InvalidInstrument(
                "symbol must use uppercase letters, digits, '-' or '_'",
            ));
        }
        if self.base == self.quote {
            return Err(DomainError::InvalidInstrument(
                "base and quote assets must differ",
            ));
        }
        if self.tick_size <= 0 || self.lot_size <= 0 {
            return Err(DomainError::InvalidInstrument(
                "tick_size and lot_size must be positive",
            ));
        }
        if self.min_qty.lots() == 0 {
            return Err(DomainError::InvalidInstrument(
                "minimum quantity must be positive",
            ));
        }
        if self.max_qty < self.min_qty {
            return Err(DomainError::InvalidInstrument(
                "maximum quantity must not be below minimum",
            ));
        }
        if self.min_notional.atomic() <= 0 {
            return Err(DomainError::InvalidInstrument(
                "minimum notional must be positive",
            ));
        }
        if self.max_notional < self.min_notional {
            return Err(DomainError::InvalidInstrument(
                "maximum notional must not be below minimum",
            ));
        }
        if self.price_band_bps < 0 || self.price_band_bps > 10_000 {
            return Err(DomainError::InvalidInstrument(
                "price band must be between 0 and 10000 bps",
            ));
        }
        if !self.maker_fee_bps.is_valid_rate() || !self.taker_fee_bps.is_valid_rate() {
            return Err(DomainError::InvalidInstrument(
                "fee rates must be within +/-10000 bps",
            ));
        }

        match (self.kind, self.contract.as_ref()) {
            (InstrumentKind::Spot, Some(_)) => Err(DomainError::InvalidInstrument(
                "spot instruments must not have a contract",
            )),
            (InstrumentKind::Perpetual, None) => Err(DomainError::InvalidInstrument(
                "perpetual instruments require a contract",
            )),
            (InstrumentKind::Perpetual, Some(contract)) => {
                if contract.settlement_asset != self.quote {
                    return Err(DomainError::InvalidInstrument(
                        "linear contract settlement asset must equal quote asset",
                    ));
                }
                contract.validate()
            }
            (InstrumentKind::Spot, None) => Ok(()),
        }
    }

    /// Convert price ticks and lot quantity to quote atomic units.
    pub fn quote_amount(
        &self,
        price: Price,
        qty: Qty,
        base_decimals: Scale,
        rounding: Rounding,
    ) -> Result<Amount, DomainError> {
        convert::quote_amount(
            price,
            qty,
            self.tick_size,
            self.lot_size,
            base_decimals,
            rounding,
        )
    }

    /// Parse a quote decimal into ticks, requiring exact tick alignment.
    pub fn price_from_decimal(
        &self,
        input: &str,
        quote_decimals: Scale,
    ) -> Result<Price, DomainError> {
        let atomic = parse_scaled(input, quote_decimals)?;
        Price::from_atomic(atomic, self.tick_size)
    }

    /// Format ticks as a canonical fixed-scale quote decimal.
    pub fn price_to_decimal(
        &self,
        price: Price,
        quote_decimals: Scale,
    ) -> Result<String, DomainError> {
        let atomic = price.to_atomic(self.tick_size)?;
        Ok(format_scaled(atomic, quote_decimals))
    }

    /// Parse a base decimal into lots, requiring exact lot alignment.
    pub fn qty_from_decimal(&self, input: &str, base_decimals: Scale) -> Result<Qty, DomainError> {
        let atomic = parse_scaled(input, base_decimals)?;
        Qty::from_atomic(atomic, self.lot_size)
    }

    /// Format lots as a canonical fixed-scale base decimal.
    pub fn qty_to_decimal(&self, qty: Qty, base_decimals: Scale) -> Result<String, DomainError> {
        let atomic = qty.to_atomic(self.lot_size)?;
        Ok(format_scaled(atomic, base_decimals))
    }

    /// Base atomic quantity represented by lots.
    pub fn base_atomic(&self, qty: Qty) -> Result<i128, DomainError> {
        base_atomic(qty, self.lot_size)
    }
}

#[cfg(test)]
mod tests {
    use super::{ContractSpec, Instrument, InstrumentKind, InstrumentStatus, MarginTier};
    use crate::decimal::Scale;
    use crate::error::DomainError;
    use crate::id::{AssetId, SymbolId};
    use crate::money::{Amount, Bps, Multiplier, Price, Qty};
    use crate::rounding::Rounding;

    fn contract() -> ContractSpec {
        ContractSpec {
            contract_size: Multiplier::ONE,
            settlement_asset: AssetId::new(2),
            max_leverage: 125,
            margin_tiers: vec![
                MarginTier {
                    notional_floor: Amount::new(0),
                    notional_cap: Amount::new(50_000_000_000),
                    max_leverage: 125,
                    mmr_bps: Bps::new(40),
                    maintenance_amount: Amount::new(0),
                },
                MarginTier {
                    notional_floor: Amount::new(50_000_000_000),
                    notional_cap: Amount::new(250_000_000_000),
                    max_leverage: 100,
                    mmr_bps: Bps::new(50),
                    maintenance_amount: Amount::new(50_000_000),
                },
                MarginTier {
                    notional_floor: Amount::new(250_000_000_000),
                    notional_cap: Amount::new(1_000_000_000_000),
                    max_leverage: 50,
                    mmr_bps: Bps::new(100),
                    maintenance_amount: Amount::new(1_300_000_000),
                },
            ],
            funding_interval_secs: 28_800,
            funding_cap_bps: 75,
            liquidation_fee_bps: Bps::new(50),
            adl_enabled: true,
        }
    }

    fn instrument() -> Instrument {
        Instrument {
            symbol_id: SymbolId::new(1),
            symbol: "BTCUSDT".to_owned(),
            kind: InstrumentKind::Perpetual,
            base: AssetId::new(1),
            quote: AssetId::new(2),
            tick_size: 100_000,
            lot_size: 100_000,
            min_qty: Qty::new(1).unwrap(),
            max_qty: Qty::new(1_000_000).unwrap(),
            min_notional: Amount::new(5_000_000),
            max_notional: Amount::new(1_000_000_000_000),
            price_band_bps: 500,
            maker_fee_bps: Bps::new(2),
            taker_fee_bps: Bps::new(5),
            contract: Some(contract()),
            status: InstrumentStatus::Active,
        }
    }

    #[test]
    fn valid_perpetual_instrument_passes() {
        assert_eq!(instrument().validate(), Ok(()));
    }

    #[test]
    fn accepts_hyphen_underscore_and_digit_symbols() {
        let mut instrument = instrument();
        instrument.symbol = "BTC2-USDT_PERP".to_owned();
        assert_eq!(instrument.validate(), Ok(()));
    }

    #[test]
    fn converts_decimal_prices_and_quantities_with_alignment() {
        let instrument = instrument();
        let price = instrument
            .price_from_decimal("60000.1", Scale::new(6).unwrap())
            .unwrap();
        assert_eq!(price.ticks(), 600_001);
        assert_eq!(
            instrument
                .price_to_decimal(price, Scale::new(6).unwrap())
                .unwrap(),
            "60000.100000"
        );
        assert_eq!(
            instrument.price_from_decimal("60000.15", Scale::new(6).unwrap()),
            Err(DomainError::PriceNotAligned)
        );

        let qty = instrument
            .qty_from_decimal("1.25", Scale::new(8).unwrap())
            .unwrap();
        assert_eq!(qty.lots(), 1_250);
        assert_eq!(
            instrument
                .qty_to_decimal(qty, Scale::new(8).unwrap())
                .unwrap(),
            "1.25000000"
        );
        assert_eq!(
            instrument.qty_from_decimal("1.2501", Scale::new(8).unwrap()),
            Err(DomainError::QtyNotAligned)
        );
        assert_eq!(instrument.base_atomic(qty).unwrap(), 125_000_000);
        assert_eq!(
            instrument
                .quote_amount(price, qty, Scale::new(8).unwrap(), Rounding::Down)
                .unwrap()
                .atomic(),
            75_000_125_000
        );
    }

    #[test]
    fn rejects_bad_instrument_fields() {
        let cases: [fn(&mut Instrument); 15] = [
            |value| value.symbol.clear(),
            |value| value.symbol = "btcusdt".to_owned(),
            |value| value.symbol = "BTC USDT".to_owned(),
            |value| value.symbol = "A".repeat(33),
            |value| value.base = value.quote,
            |value| value.tick_size = 0,
            |value| value.lot_size = 0,
            |value| value.min_qty = Qty::ZERO,
            |value| value.max_qty = Qty::ZERO,
            |value| value.min_notional = Amount::ZERO,
            |value| value.max_notional = Amount::new(1),
            |value| value.price_band_bps = -1,
            |value| value.price_band_bps = 10_001,
            |value| value.maker_fee_bps = Bps::new(10_001),
            |value| value.taker_fee_bps = Bps::new(-10_001),
        ];
        for mutate in cases {
            let mut value = instrument();
            mutate(&mut value);
            assert!(value.validate().is_err());
        }
    }

    #[test]
    fn rejects_contract_mismatches() {
        let mut spot = instrument();
        spot.kind = InstrumentKind::Spot;
        assert_eq!(
            spot.validate(),
            Err(DomainError::InvalidInstrument(
                "spot instruments must not have a contract"
            ))
        );

        let mut no_contract = instrument();
        no_contract.contract = None;
        assert_eq!(
            no_contract.validate(),
            Err(DomainError::InvalidInstrument(
                "perpetual instruments require a contract"
            ))
        );

        let mut wrong_settlement = instrument();
        wrong_settlement
            .contract
            .as_mut()
            .expect("contract")
            .settlement_asset = AssetId::new(99);
        assert_eq!(
            wrong_settlement.validate(),
            Err(DomainError::InvalidInstrument(
                "linear contract settlement asset must equal quote asset"
            ))
        );
    }

    #[test]
    fn rejects_overflowing_margin_tier_derivation() {
        let mut overflow = contract();
        overflow.margin_tiers = vec![
            MarginTier {
                notional_floor: Amount::ZERO,
                notional_cap: Amount::new(i128::MAX - 1),
                max_leverage: 125,
                mmr_bps: Bps::new(0),
                maintenance_amount: Amount::ZERO,
            },
            MarginTier {
                notional_floor: Amount::new(i128::MAX - 1),
                notional_cap: Amount::new(i128::MAX),
                max_leverage: 100,
                mmr_bps: Bps::new(10_000),
                maintenance_amount: Amount::ZERO,
            },
        ];
        assert_eq!(overflow.validate(), Err(DomainError::Overflow));
    }

    #[test]
    fn rejects_invalid_size_during_decimal_formatting() {
        let mut invalid = instrument();
        invalid.tick_size = 0;
        assert_eq!(
            invalid.price_to_decimal(Price::new(1).unwrap(), Scale::ZERO),
            Err(DomainError::InvalidInstrument("tick_size must be positive"))
        );

        invalid.lot_size = 0;
        assert_eq!(
            invalid.qty_to_decimal(Qty::new(1).unwrap(), Scale::ZERO),
            Err(DomainError::InvalidInstrument("lot_size must be positive"))
        );
    }

    #[test]
    fn validates_margin_tier_continuity() {
        let valid = contract();
        assert_eq!(valid.validate(), Ok(()));

        let mut gap = valid.clone();
        gap.margin_tiers[1].notional_floor = Amount::new(1);
        assert!(gap.validate().is_err());

        let mut wrong_amount = valid.clone();
        wrong_amount.margin_tiers[1].maintenance_amount = Amount::new(1);
        assert!(wrong_amount.validate().is_err());

        let mut decreasing_rate = valid.clone();
        decreasing_rate.margin_tiers[2].mmr_bps = Bps::new(10);
        assert!(decreasing_rate.validate().is_err());

        let mut first_floor = valid.clone();
        first_floor.margin_tiers[0].notional_floor = Amount::new(1);
        assert!(first_floor.validate().is_err());

        let mut first_amount = valid.clone();
        first_amount.margin_tiers[0].maintenance_amount = Amount::new(1);
        assert!(first_amount.validate().is_err());

        let mut too_much_leverage = valid.clone();
        too_much_leverage.margin_tiers[1].max_leverage = 126;
        assert!(too_much_leverage.validate().is_err());

        let mut leverage_increase = valid.clone();
        leverage_increase.margin_tiers[1].max_leverage = 50;
        leverage_increase.margin_tiers[2].max_leverage = 100;
        assert!(leverage_increase.validate().is_err());

        let mut non_integral = valid;
        non_integral.margin_tiers[1].notional_floor = Amount::new(50_000_000_001);
        non_integral.margin_tiers[0].notional_cap = Amount::new(50_000_000_001);
        non_integral.margin_tiers[2].notional_floor = Amount::new(250_000_000_000);
        assert_eq!(
            non_integral.validate(),
            Err(DomainError::InvalidContract(
                "maintenance amount must be integral"
            ))
        );
    }

    #[test]
    fn validates_margin_tier_field_ranges() {
        let valid = contract().margin_tiers[1];
        assert_eq!(valid.validate(), Ok(()));

        let mut tier = valid;
        tier.notional_floor = Amount::new(-1);
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.notional_cap = Amount::new(-1);
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.notional_cap = tier.notional_floor;
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.max_leverage = 0;
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.mmr_bps = Bps::new(-1);
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.mmr_bps = Bps::new(10_001);
        assert!(tier.validate().is_err());

        let mut tier = valid;
        tier.maintenance_amount = Amount::new(-1);
        assert!(tier.validate().is_err());
    }

    #[test]
    fn validates_contract_ranges_and_tiers() {
        let mut empty = contract();
        empty.margin_tiers.clear();
        assert!(empty.validate().is_err());

        let mut funding = contract();
        funding.funding_interval_secs = 0;
        assert!(funding.validate().is_err());

        let mut funding = contract();
        funding.funding_interval_secs = 604_801;
        assert!(funding.validate().is_err());

        let mut cap = contract();
        cap.funding_cap_bps = -1;
        assert!(cap.validate().is_err());

        let mut cap = contract();
        cap.funding_cap_bps = 10_001;
        assert!(cap.validate().is_err());

        let mut liquidation = contract();
        liquidation.liquidation_fee_bps = Bps::new(-1);
        assert!(liquidation.validate().is_err());

        let mut liquidation = contract();
        liquidation.liquidation_fee_bps = Bps::new(10_001);
        assert!(liquidation.validate().is_err());

        let mut leverage = contract();
        leverage.max_leverage = 0;
        assert!(leverage.validate().is_err());

        let mut tier = contract();
        tier.margin_tiers[0].max_leverage = 0;
        assert!(tier.validate().is_err());
    }

    #[test]
    fn serde_round_trips_instrument() {
        let instrument = instrument();
        let encoded = serde_json::to_string(&instrument).unwrap();
        let decoded: Instrument = serde_json::from_str(&encoded).unwrap();
        assert_eq!(decoded, instrument);
        assert_eq!(decoded.validate(), Ok(()));
    }
}
