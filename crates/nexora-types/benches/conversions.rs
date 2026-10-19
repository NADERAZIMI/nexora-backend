use std::hint::black_box;

use criterion::{Criterion, criterion_group, criterion_main};
use nexora_types::{
    Amount, Bps, Price, Qty, Rounding, Scale, format_scaled, parse_scaled, quote_amount,
};

fn conversions(c: &mut Criterion) {
    let price = Price::new(600_001).expect("valid price");
    let qty = Qty::new(1_250).expect("valid quantity");
    let scale = Scale::new(8).expect("valid scale");
    let value = 75_000_125_000_i128;

    c.bench_function("quote_amount_ticks_lots", |b| {
        b.iter(|| {
            quote_amount(
                black_box(price),
                black_box(qty),
                black_box(100_000),
                black_box(100_000),
                black_box(scale),
                black_box(Rounding::Down),
            )
            .expect("benchmark values cannot overflow")
        })
    });

    c.bench_function("decimal_parse_amount", |b| {
        b.iter(|| parse_scaled(black_box("60000.100000"), black_box(scale)).expect("valid decimal"))
    });

    c.bench_function("decimal_format_amount", |b| {
        b.iter(|| format_scaled(black_box(value), black_box(scale)))
    });

    c.bench_function("amount_mul_bps", |b| {
        b.iter(|| {
            Amount::new(black_box(value))
                .checked_mul_bps(black_box(Bps::new(5)), black_box(Rounding::Up))
                .expect("benchmark values cannot overflow")
        })
    });
}

criterion_group!(benches, conversions);
criterion_main!(benches);
