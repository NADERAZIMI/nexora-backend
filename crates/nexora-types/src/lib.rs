#![doc = include_str!("../README.md")]

pub mod convert;
pub mod decimal;
pub mod error;
pub mod fee;
pub mod id;
pub mod instrument;
pub mod money;
pub mod rounding;
pub mod time;

pub use convert::{base_atomic, quote_amount};
pub use decimal::{MAX_SCALE, Scale, format_scaled, parse_scaled};
pub use error::{DomainError, ErrorCode};
pub use fee::FeeSchedule;
pub use id::{AccountId, AssetId, HoldId, JournalId, OrderId, SymbolId, TradeId};
pub use instrument::{ContractSpec, Instrument, InstrumentKind, InstrumentStatus, MarginTier};
pub use money::{Amount, Bps, Multiplier, Price, Qty};
pub use rounding::{Rounding, div_round, mul_div};
pub use time::Timestamp;
