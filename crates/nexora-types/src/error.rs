//! Stable error taxonomy shared by all NEXORA services.
//!
//! Public error codes are machine-readable and stable. New codes are additive;
//! existing code strings must never be reused for a different meaning. HTTP
//! mapping and retryability live here so REST, gRPC, and WebSocket edges cannot
//! drift apart.

use std::fmt;

use thiserror::Error;

/// A stable public error code.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum ErrorCode {
    AuthMissing,
    AuthInvalidSignature,
    AuthTimestamp,
    AuthScope,
    AuthIpNotAllowed,

    AccFrozen,
    AccRestricted,
    AccCurrencyMismatch,
    AccDuplicateSettlement,
    AccHoldNotFound,
    AccInsufficientAvailable,
    AccNotFound,
    AccReconciliationBlock,
    AccShardUnavailable,

    ValSchema,
    ValDecimal,
    ValScale,
    ValPriceTick,
    ValQtyLot,
    ValQtyRange,
    ValNotionalRange,
    ValTif,
    ValSlippageRequired,
    ValIdempotencyConflict,
    ValOverflow,
    ValPositionSide,

    MktUnknown,
    MktHalted,
    MktPriceBand,
    MktNoLiquidity,
    MktBookFull,

    OrdUnknown,
    OrdIdempotencyConflict,
    OrdPostOnlyWouldCross,
    OrdReduceOnly,
    OrdReduceOnlyNoPosition,
    OrdNoPosition,

    RskInsufficientBalance,
    RskInsufficientMargin,
    RskLeverageExceeded,
    RskPositionLimit,
    RskMaxOpenOrders,
    RskOrderLimit,
    RskAccountRestricted,
    RskSymbolRestricted,

    WalInsufficientConfirmations,

    SysBackpressure,
    SysShardUnavailable,
    SysInProgress,
    SysMaintenance,
    SysConflict,

    RateLimitExceeded,
    RateOrderRate,
    RateCancelRatio,
}

impl ErrorCode {
    /// Every code in the registry. Used by conformance tests and generated
    /// documentation.
    pub const ALL: &'static [Self] = &[
        Self::AuthMissing,
        Self::AuthInvalidSignature,
        Self::AuthTimestamp,
        Self::AuthScope,
        Self::AuthIpNotAllowed,
        Self::AccFrozen,
        Self::AccRestricted,
        Self::AccCurrencyMismatch,
        Self::AccDuplicateSettlement,
        Self::AccHoldNotFound,
        Self::AccInsufficientAvailable,
        Self::AccNotFound,
        Self::AccReconciliationBlock,
        Self::AccShardUnavailable,
        Self::ValSchema,
        Self::ValDecimal,
        Self::ValScale,
        Self::ValPriceTick,
        Self::ValQtyLot,
        Self::ValQtyRange,
        Self::ValNotionalRange,
        Self::ValTif,
        Self::ValSlippageRequired,
        Self::ValIdempotencyConflict,
        Self::ValOverflow,
        Self::ValPositionSide,
        Self::MktUnknown,
        Self::MktHalted,
        Self::MktPriceBand,
        Self::MktNoLiquidity,
        Self::MktBookFull,
        Self::OrdUnknown,
        Self::OrdIdempotencyConflict,
        Self::OrdPostOnlyWouldCross,
        Self::OrdReduceOnly,
        Self::OrdReduceOnlyNoPosition,
        Self::OrdNoPosition,
        Self::RskInsufficientBalance,
        Self::RskInsufficientMargin,
        Self::RskLeverageExceeded,
        Self::RskPositionLimit,
        Self::RskMaxOpenOrders,
        Self::RskOrderLimit,
        Self::RskAccountRestricted,
        Self::RskSymbolRestricted,
        Self::WalInsufficientConfirmations,
        Self::SysBackpressure,
        Self::SysShardUnavailable,
        Self::SysInProgress,
        Self::SysMaintenance,
        Self::SysConflict,
        Self::RateLimitExceeded,
        Self::RateOrderRate,
        Self::RateCancelRatio,
    ];

    /// Stable wire representation. Never change an existing value.
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::AuthMissing => "AUTH_MISSING",
            Self::AuthInvalidSignature => "AUTH_INVALID_SIGNATURE",
            Self::AuthTimestamp => "AUTH_TIMESTAMP",
            Self::AuthScope => "AUTH_SCOPE",
            Self::AuthIpNotAllowed => "AUTH_IP_NOT_ALLOWED",
            Self::AccFrozen => "ACC_FROZEN",
            Self::AccRestricted => "ACC_RESTRICTED",
            Self::AccCurrencyMismatch => "ACC_CURRENCY_MISMATCH",
            Self::AccDuplicateSettlement => "ACC_DUPLICATE_SETTLEMENT",
            Self::AccHoldNotFound => "ACC_HOLD_NOT_FOUND",
            Self::AccInsufficientAvailable => "ACC_INSUFFICIENT_AVAILABLE",
            Self::AccNotFound => "ACC_NOT_FOUND",
            Self::AccReconciliationBlock => "ACC_RECONCILIATION_BLOCK",
            Self::AccShardUnavailable => "ACC_SHARD_UNAVAILABLE",
            Self::ValSchema => "VAL_SCHEMA",
            Self::ValDecimal => "VAL_DECIMAL",
            Self::ValScale => "VAL_SCALE",
            Self::ValPriceTick => "VAL_PRICE_TICK",
            Self::ValQtyLot => "VAL_QTY_LOT",
            Self::ValQtyRange => "VAL_QTY_RANGE",
            Self::ValNotionalRange => "VAL_NOTIONAL_RANGE",
            Self::ValTif => "VAL_TIF",
            Self::ValSlippageRequired => "VAL_SLIPPAGE_REQUIRED",
            Self::ValIdempotencyConflict => "VAL_IDEMPOTENCY_CONFLICT",
            Self::ValOverflow => "VAL_OVERFLOW",
            Self::ValPositionSide => "VAL_POSITION_SIDE",
            Self::MktUnknown => "MKT_UNKNOWN",
            Self::MktHalted => "MKT_HALTED",
            Self::MktPriceBand => "MKT_PRICE_BAND",
            Self::MktNoLiquidity => "MKT_NO_LIQUIDITY",
            Self::MktBookFull => "MKT_BOOK_FULL",
            Self::OrdUnknown => "ORD_UNKNOWN",
            Self::OrdIdempotencyConflict => "ORD_IDEMPOTENCY_CONFLICT",
            Self::OrdPostOnlyWouldCross => "ORD_POST_ONLY_WOULD_CROSS",
            Self::OrdReduceOnly => "ORD_REDUCE_ONLY",
            Self::OrdReduceOnlyNoPosition => "ORD_REDUCE_ONLY_NO_POSITION",
            Self::OrdNoPosition => "ORD_NO_POSITION",
            Self::RskInsufficientBalance => "RSK_INSUFFICIENT_BALANCE",
            Self::RskInsufficientMargin => "RSK_INSUFFICIENT_MARGIN",
            Self::RskLeverageExceeded => "RSK_LEVERAGE_EXCEEDED",
            Self::RskPositionLimit => "RSK_POSITION_LIMIT",
            Self::RskMaxOpenOrders => "RSK_MAX_OPEN_ORDERS",
            Self::RskOrderLimit => "RSK_ORDER_LIMIT",
            Self::RskAccountRestricted => "RSK_ACCOUNT_RESTRICTED",
            Self::RskSymbolRestricted => "RSK_SYMBOL_RESTRICTED",
            Self::WalInsufficientConfirmations => "WAL_INSUFFICIENT_CONFIRMATIONS",
            Self::SysBackpressure => "SYS_BACKPRESSURE",
            Self::SysShardUnavailable => "SYS_SHARD_UNAVAILABLE",
            Self::SysInProgress => "SYS_IN_PROGRESS",
            Self::SysMaintenance => "SYS_MAINTENANCE",
            Self::SysConflict => "SYS_CONFLICT",
            Self::RateLimitExceeded => "RATE_LIMIT_EXCEEDED",
            Self::RateOrderRate => "RATE_ORDER_RATE",
            Self::RateCancelRatio => "RATE_CANCEL_RATIO",
        }
    }

    /// HTTP status used by the edge mapping.
    pub const fn http_status(self) -> u16 {
        match self {
            Self::AuthMissing | Self::AuthInvalidSignature | Self::AuthTimestamp => 401,
            Self::AuthScope
            | Self::AuthIpNotAllowed
            | Self::AccFrozen
            | Self::AccRestricted
            | Self::RskAccountRestricted
            | Self::RskSymbolRestricted => 403,
            Self::AccHoldNotFound | Self::AccNotFound | Self::MktUnknown | Self::OrdUnknown => 404,
            Self::AccCurrencyMismatch
            | Self::AccDuplicateSettlement
            | Self::AccInsufficientAvailable
            | Self::AccReconciliationBlock
            | Self::ValIdempotencyConflict
            | Self::MktHalted
            | Self::MktNoLiquidity
            | Self::OrdIdempotencyConflict
            | Self::WalInsufficientConfirmations
            | Self::SysInProgress
            | Self::SysConflict => 409,
            Self::RateLimitExceeded | Self::RateOrderRate | Self::RateCancelRatio => 429,
            Self::AccShardUnavailable
            | Self::MktBookFull
            | Self::SysBackpressure
            | Self::SysShardUnavailable
            | Self::SysMaintenance => 503,
            Self::ValSchema
            | Self::ValDecimal
            | Self::ValScale
            | Self::ValPriceTick
            | Self::ValQtyLot
            | Self::ValQtyRange
            | Self::ValNotionalRange
            | Self::ValTif
            | Self::ValSlippageRequired
            | Self::ValOverflow
            | Self::ValPositionSide
            | Self::MktPriceBand
            | Self::OrdPostOnlyWouldCross
            | Self::OrdReduceOnly
            | Self::OrdReduceOnlyNoPosition
            | Self::OrdNoPosition
            | Self::RskInsufficientBalance
            | Self::RskInsufficientMargin
            | Self::RskLeverageExceeded
            | Self::RskPositionLimit
            | Self::RskMaxOpenOrders
            | Self::RskOrderLimit => 400,
        }
    }

    /// Whether a client may retry the same request after the named condition
    /// changes. Ambiguous infrastructure failures still require an idempotency
    /// query rather than a blind retry.
    pub const fn retryable(self) -> bool {
        matches!(
            self,
            Self::AccInsufficientAvailable
                | Self::AccShardUnavailable
                | Self::MktHalted
                | Self::MktPriceBand
                | Self::MktNoLiquidity
                | Self::MktBookFull
                | Self::RskInsufficientBalance
                | Self::RskInsufficientMargin
                | Self::WalInsufficientConfirmations
                | Self::SysBackpressure
                | Self::SysShardUnavailable
                | Self::SysInProgress
                | Self::SysMaintenance
                | Self::SysConflict
                | Self::RateLimitExceeded
                | Self::RateOrderRate
                | Self::RateCancelRatio
        )
    }

    /// An ambiguous result must be resolved by querying the idempotency key.
    pub const fn is_ambiguous(self) -> bool {
        matches!(
            self,
            Self::SysShardUnavailable | Self::SysInProgress | Self::SysConflict
        )
    }

    /// Human-readable text for operator tooling. Public API messages may add
    /// context but must not leak secrets.
    pub const fn description(self) -> &'static str {
        match self {
            Self::AuthMissing => "Missing credentials",
            Self::AuthInvalidSignature => "Signature mismatch",
            Self::AuthTimestamp => "Timestamp outside the accepted window",
            Self::AuthScope => "Credential lacks the required scope",
            Self::AuthIpNotAllowed => "IP address is not allowlisted",
            Self::AccFrozen => "Account is frozen",
            Self::AccRestricted => "Account or product is restricted",
            Self::AccCurrencyMismatch => "Asset currency does not match",
            Self::AccDuplicateSettlement => "Settlement was already applied",
            Self::AccHoldNotFound => "Hold was not found",
            Self::AccInsufficientAvailable => "Insufficient available balance",
            Self::AccNotFound => "Account was not found",
            Self::AccReconciliationBlock => "Account is blocked by reconciliation",
            Self::AccShardUnavailable => "Account shard is unavailable",
            Self::ValSchema => "Malformed or invalid request data",
            Self::ValDecimal => "Invalid decimal string",
            Self::ValScale => "Too many decimal places",
            Self::ValPriceTick => "Price is not aligned to the instrument tick",
            Self::ValQtyLot => "Quantity is not aligned to the instrument lot",
            Self::ValQtyRange => "Quantity is outside instrument limits",
            Self::ValNotionalRange => "Notional is outside instrument limits",
            Self::ValTif => "Unsupported time in force",
            Self::ValSlippageRequired => "Slippage protection is required",
            Self::ValIdempotencyConflict => "Idempotency key was reused with a different payload",
            Self::ValOverflow => "Arithmetic overflow",
            Self::ValPositionSide => "Invalid position side",
            Self::MktUnknown => "Unknown symbol",
            Self::MktHalted => "Symbol is halted",
            Self::MktPriceBand => "Price is outside the permitted band",
            Self::MktNoLiquidity => "Insufficient liquidity",
            Self::MktBookFull => "Order book capacity exhausted",
            Self::OrdUnknown => "Unknown order",
            Self::OrdIdempotencyConflict => "Client order ID conflict",
            Self::OrdPostOnlyWouldCross => "Post-only order would cross",
            Self::OrdReduceOnly => "Reduce-only violation",
            Self::OrdReduceOnlyNoPosition => "Reduce-only order has no position",
            Self::OrdNoPosition => "Position was not found",
            Self::RskInsufficientBalance => "Insufficient balance",
            Self::RskInsufficientMargin => "Insufficient margin",
            Self::RskLeverageExceeded => "Leverage exceeds the permitted maximum",
            Self::RskPositionLimit => "Position limit exceeded",
            Self::RskMaxOpenOrders => "Open-order limit exceeded",
            Self::RskOrderLimit => "Order notional limit exceeded",
            Self::RskAccountRestricted => "Account is restricted by risk policy",
            Self::RskSymbolRestricted => "Symbol is restricted by risk policy",
            Self::WalInsufficientConfirmations => "Insufficient blockchain confirmations",
            Self::SysBackpressure => "Service is overloaded",
            Self::SysShardUnavailable => "Shard leader is unavailable",
            Self::SysInProgress => "Duplicate request is already in flight",
            Self::SysMaintenance => "Scheduled maintenance",
            Self::SysConflict => "Concurrent state conflict",
            Self::RateLimitExceeded => "Rate limit exceeded",
            Self::RateOrderRate => "Order rate limit exceeded",
            Self::RateCancelRatio => "Cancel ratio limit exceeded",
        }
    }
}

impl fmt::Display for ErrorCode {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

/// Typed domain error carrying a stable public code.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum DomainError {
    #[error("invalid decimal string")]
    InvalidDecimal,
    #[error("decimal places {actual} exceed scale {max}")]
    ScaleExceeded { max: u8, actual: u8 },
    #[error("scale {0} is invalid; maximum is 18")]
    InvalidScale(u8),
    #[error("arithmetic overflow")]
    Overflow,
    #[error("division by zero")]
    DivisionByZero,
    #[error("negative value is not allowed")]
    Negative,
    #[error("invalid identifier: {0}")]
    InvalidId(&'static str),
    #[error("UUID must be version 7, got version {0}")]
    InvalidUuidVersion(usize),
    #[error("timestamp is outside the supported range")]
    InvalidTimestamp,
    #[error("invalid instrument: {0}")]
    InvalidInstrument(&'static str),
    #[error("invalid contract: {0}")]
    InvalidContract(&'static str),
    #[error("invalid margin tier: {0}")]
    InvalidMarginTier(&'static str),
    #[error("invalid fee schedule: {0}")]
    InvalidFeeSchedule(&'static str),
    #[error("invalid multiplier")]
    InvalidMultiplier,
    #[error("price is not aligned to the instrument tick")]
    PriceNotAligned,
    #[error("quantity is not aligned to the instrument lot")]
    QtyNotAligned,
    #[error("value is outside the allowed range")]
    OutOfRange,
}

impl DomainError {
    /// Stable public code for this error.
    pub const fn code(&self) -> ErrorCode {
        match self {
            Self::InvalidDecimal => ErrorCode::ValDecimal,
            Self::ScaleExceeded { .. } | Self::InvalidScale(_) => ErrorCode::ValScale,
            Self::Overflow => ErrorCode::ValOverflow,
            Self::PriceNotAligned => ErrorCode::ValPriceTick,
            Self::QtyNotAligned => ErrorCode::ValQtyLot,
            Self::DivisionByZero
            | Self::Negative
            | Self::InvalidId(_)
            | Self::InvalidUuidVersion(_)
            | Self::InvalidTimestamp
            | Self::InvalidInstrument(_)
            | Self::InvalidContract(_)
            | Self::InvalidMarginTier(_)
            | Self::InvalidFeeSchedule(_)
            | Self::InvalidMultiplier
            | Self::OutOfRange => ErrorCode::ValSchema,
        }
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeSet;

    use super::{DomainError, ErrorCode};

    #[test]
    fn every_code_is_unique_and_prefixed() {
        let mut codes = BTreeSet::new();
        for code in ErrorCode::ALL {
            let text = code.as_str();
            assert!(codes.insert(text), "duplicate error code {text}");
            assert!(
                text.starts_with("AUTH_")
                    || text.starts_with("ACC_")
                    || text.starts_with("VAL_")
                    || text.starts_with("MKT_")
                    || text.starts_with("ORD_")
                    || text.starts_with("RSK_")
                    || text.starts_with("WAL_")
                    || text.starts_with("SYS_")
                    || text.starts_with("RATE_"),
                "unexpected prefix for {text}"
            );
            assert!(!code.description().is_empty());
            assert!(matches!(
                code.http_status(),
                400 | 401 | 403 | 404 | 409 | 429 | 503
            ));
        }
        assert_eq!(codes.len(), ErrorCode::ALL.len());
    }

    #[test]
    fn documented_codes_keep_their_contract() {
        assert_eq!(ErrorCode::AuthInvalidSignature.http_status(), 401);
        assert_eq!(ErrorCode::ValPriceTick.http_status(), 400);
        assert_eq!(ErrorCode::SysShardUnavailable.http_status(), 503);
        assert!(ErrorCode::SysShardUnavailable.retryable());
        assert!(ErrorCode::SysShardUnavailable.is_ambiguous());
        assert_eq!(ErrorCode::RateLimitExceeded.http_status(), 429);
        assert_eq!(ErrorCode::OrdUnknown.http_status(), 404);
        assert!(!ErrorCode::OrdUnknown.retryable());
    }

    #[test]
    fn domain_errors_map_to_stable_codes() {
        assert_eq!(DomainError::InvalidDecimal.code(), ErrorCode::ValDecimal);
        assert_eq!(
            DomainError::ScaleExceeded { max: 6, actual: 7 }.code(),
            ErrorCode::ValScale
        );
        assert_eq!(DomainError::Overflow.code(), ErrorCode::ValOverflow);
        assert_eq!(DomainError::DivisionByZero.code(), ErrorCode::ValSchema);
        assert_eq!(DomainError::PriceNotAligned.code(), ErrorCode::ValPriceTick);
        assert_eq!(DomainError::QtyNotAligned.code(), ErrorCode::ValQtyLot);
        assert_eq!(
            DomainError::InvalidInstrument("bad").code(),
            ErrorCode::ValSchema
        );
    }
}
