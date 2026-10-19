//! Release-mode ledger-style invariant gate.
//!
//! Run with:
//! `cargo test -p nexora-types --release --test invariants -- --ignored`

use nexora_types::Amount;

const ACCOUNTS: usize = 8;
const OPERATIONS: usize = 10_000_000;

#[test]
#[ignore = "10M-operation gate; run with --release --ignored"]
fn ten_million_randomized_operations_preserve_ledger_invariants() {
    let mut rng = XorShift64::new(0x4e45_584f_5241_0002);
    let mut balances = [Amount::ZERO; ACCOUNTS];
    let mut journal_total = Amount::ZERO;

    for _ in 0..OPERATIONS {
        let from = usize::try_from(rng.next() % 8).expect("bounded index");
        let mut to = usize::try_from(rng.next() % 8).expect("bounded index");
        if to == from {
            to = (to + 1) % ACCOUNTS;
        }

        // A positive transfer creates two equal and opposite journal legs.
        let amount = Amount::new(i128::from(rng.next() % 1_000_000) + 1);
        balances[from] = balances[from]
            .checked_sub(amount)
            .expect("bounded transfer cannot overflow");
        balances[to] = balances[to]
            .checked_add(amount)
            .expect("bounded transfer cannot overflow");
        journal_total = journal_total
            .checked_sub(amount)
            .and_then(|value| value.checked_add(amount))
            .expect("mirrored journal legs cannot overflow");

        let mut account_total = Amount::ZERO;
        for balance in balances {
            account_total = account_total
                .checked_add(balance)
                .expect("bounded account balances cannot overflow");
        }
        assert_eq!(account_total, Amount::ZERO, "account zero-sum invariant");
        assert_eq!(journal_total, Amount::ZERO, "journal zero-sum invariant");
    }
}

struct XorShift64(u64);

impl XorShift64 {
    const fn new(seed: u64) -> Self {
        Self(seed)
    }

    fn next(&mut self) -> u64 {
        let mut value = self.0;
        value ^= value << 13;
        value ^= value >> 7;
        value ^= value << 17;
        self.0 = value;
        value
    }
}
