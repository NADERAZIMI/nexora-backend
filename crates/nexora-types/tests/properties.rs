use nexora_types::{
    AccountId, Amount, AssetId, Bps, OrderId, Price, Qty, Rounding, Scale, Timestamp, base_atomic,
    div_round, format_scaled, parse_scaled, quote_amount,
};
use proptest::prelude::*;

proptest! {
    #![proptest_config(ProptestConfig::with_cases(512))]

    #[test]
    fn decimal_format_parse_round_trips(value: i128, scale in 0_u8..=18) {
        let scale = Scale::new(scale).expect("generated scale is valid");
        let encoded = format_scaled(value, scale);
        prop_assert_eq!(parse_scaled(&encoded, scale).unwrap(), value);
    }

    #[test]
    fn decimal_parser_never_panics(input in "\\PC{0,80}", scale in 0_u8..=18) {
        let scale = Scale::new(scale).expect("generated scale is valid");
        let _ = parse_scaled(&input, scale);
    }

    #[test]
    fn rounding_is_consistent_with_truncation(
        numerator: i128,
        denominator: i128,
    ) {
        prop_assume!(denominator != 0);
        prop_assume!(!(numerator == i128::MIN && denominator == -1));

        let down = div_round(numerator, denominator, Rounding::Down).unwrap();
        let up = div_round(numerator, denominator, Rounding::Up).unwrap();
        let half = div_round(numerator, denominator, Rounding::HalfUp).unwrap();
        let truncated = numerator / denominator;
        let remainder = numerator % denominator;

        prop_assert_eq!(down, truncated);
        if remainder == 0 {
            prop_assert_eq!(up, truncated);
            prop_assert_eq!(half, truncated);
        } else {
            prop_assert!(up.unsigned_abs() > down.unsigned_abs());
            prop_assert!(up.unsigned_abs() - down.unsigned_abs() == 1);
            let twice = remainder.unsigned_abs().checked_mul(2).unwrap();
            if twice >= denominator.unsigned_abs() {
                prop_assert_eq!(half, up);
            } else {
                prop_assert_eq!(half, down);
            }
        }
    }

    #[test]
    fn conversion_matches_rational_formula(
        price_ticks in 0_i64..1_000_000,
        lots in 0_i64..1_000_000,
        tick_size in 1_i64..1_000_000,
        lot_size in 1_i64..1_000_000,
        scale in 0_u8..=8,
        rounding in prop_oneof![Just(Rounding::HalfUp), Just(Rounding::Up), Just(Rounding::Down)],
    ) {
        let scale = Scale::new(scale).expect("generated scale is valid");
        let price = Price::new(price_ticks).unwrap();
        let qty = Qty::new(lots).unwrap();
        let actual = quote_amount(price, qty, tick_size, lot_size, scale, rounding).unwrap();
        let product = i128::from(price_ticks)
            .checked_mul(i128::from(tick_size))
            .unwrap()
            .checked_mul(i128::from(lots))
            .unwrap()
            .checked_mul(i128::from(lot_size))
            .unwrap();
        let expected = div_round(product, scale.factor_i128().unwrap(), rounding).unwrap();
        prop_assert_eq!(actual.atomic(), expected);
        prop_assert!(actual.atomic() >= 0);
    }

    #[test]
    fn base_atomic_round_trips(lots in 0_i64..1_000_000, lot_size in 1_i64..1_000_000) {
        let qty = Qty::new(lots).unwrap();
        let atomic = base_atomic(qty, lot_size).unwrap();
        prop_assert_eq!(Qty::from_atomic(atomic, lot_size).unwrap(), qty);
    }

    #[test]
    fn amount_bps_arithmetic_is_checked(amount: i128, bps: i32, rounding in prop_oneof![
        Just(Rounding::HalfUp), Just(Rounding::Up), Just(Rounding::Down)
    ]) {
        let amount = Amount::new(amount);
        let bps = Bps::new(bps);
        if let Ok(value) = amount.checked_mul_bps(bps, rounding) {
            let product = amount.atomic().checked_mul(i128::from(bps.get()));
            if let Some(product) = product {
                prop_assert_eq!(
                    value.atomic(),
                    div_round(product, 10_000, rounding).unwrap()
                );
            }
        }
    }

    #[test]
    fn numeric_ids_round_trip(value: u64) {
        let order = OrderId::new(value);
        prop_assert_eq!(order.to_string().parse::<OrderId>().unwrap(), order);
        let asset_value = u32::try_from(value % 4_294_967_296).unwrap();
        let asset = AssetId::new(asset_value);
        prop_assert_eq!(asset.to_string().parse::<AssetId>().unwrap(), asset);
    }

    #[test]
    fn uuid_ids_round_trip(_seed: u64) {
        let account = AccountId::generate();
        prop_assert_eq!(account.to_string().parse::<AccountId>().unwrap(), account);
        prop_assert_eq!(account.get().get_version_num(), 7);
    }

    #[test]
    fn timestamps_iso_round_trip(nanos: i64) {
        let timestamp = Timestamp::from_unix_nanos(nanos);
        let text = timestamp.to_iso8601().unwrap();
        prop_assert_eq!(Timestamp::from_iso8601(&text).unwrap(), timestamp);
    }

    #[test]
    fn money_serde_round_trips(price: i64, qty: i64, amount: i128, bps: i32) {
        let price = Price::new(i64::try_from(price.unsigned_abs()).unwrap_or(i64::MAX)).unwrap();
        let qty = Qty::new(i64::try_from(qty.unsigned_abs()).unwrap_or(i64::MAX)).unwrap();
        let amount = Amount::new(amount);
        let bps = Bps::new(bps);

        let encoded = serde_json::to_string(&price).unwrap();
        prop_assert_eq!(serde_json::from_str::<Price>(&encoded).unwrap(), price);
        let encoded = serde_json::to_string(&qty).unwrap();
        prop_assert_eq!(serde_json::from_str::<Qty>(&encoded).unwrap(), qty);
        let encoded = serde_json::to_string(&amount).unwrap();
        prop_assert_eq!(serde_json::from_str::<Amount>(&encoded).unwrap(), amount);
        let encoded = serde_json::to_string(&bps).unwrap();
        prop_assert_eq!(serde_json::from_str::<Bps>(&encoded).unwrap(), bps);
    }
}
