//! Time types: Unix nanoseconds internally, ISO-8601 UTC at the edge.
//!
//! Timestamps are UTC only. Parsing accepts an RFC3339 offset and normalizes it
//! to UTC; formatting always emits UTC. `Timestamp` is a plain `i64` in serde
//! so internal event and database payloads stay exact and compact. API DTOs
//! should use [`Timestamp::to_iso8601`] at the edge.

use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use time::UtcOffset;
use time::format_description::well_known::Rfc3339;

use crate::error::DomainError;

/// Unix timestamp in nanoseconds. Negative values are before 1970-01-01 UTC.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Timestamp(i64);

impl Timestamp {
    pub const UNIX_EPOCH: Self = Self(0);

    pub const fn from_unix_nanos(nanos: i64) -> Self {
        Self(nanos)
    }

    pub const fn as_unix_nanos(self) -> i64 {
        self.0
    }

    pub fn from_unix_millis(millis: i64) -> Result<Self, DomainError> {
        millis
            .checked_mul(1_000_000)
            .map(Self)
            .ok_or(DomainError::InvalidTimestamp)
    }

    /// Milliseconds since the Unix epoch, rounded toward negative infinity.
    pub const fn as_unix_millis(self) -> i64 {
        self.0.div_euclid(1_000_000)
    }

    /// Parse an RFC3339/ISO-8601 instant and normalize it to UTC.
    pub fn from_iso8601(input: &str) -> Result<Self, DomainError> {
        let parsed =
            OffsetDateTime::parse(input, &Rfc3339).map_err(|_| DomainError::InvalidTimestamp)?;
        let nanos = parsed.unix_timestamp_nanos();
        i64::try_from(nanos)
            .map(Self)
            .map_err(|_| DomainError::InvalidTimestamp)
    }

    /// Format this instant as RFC3339 in UTC.
    pub fn to_iso8601(self) -> Result<String, DomainError> {
        let date_time = OffsetDateTime::from_unix_timestamp_nanos(i128::from(self.0))
            .map_err(|_| DomainError::InvalidTimestamp)?
            .to_offset(UtcOffset::UTC);
        date_time
            .format(&Rfc3339)
            .map_err(|_| DomainError::InvalidTimestamp)
    }

    pub fn checked_add_nanos(self, delta: i64) -> Result<Self, DomainError> {
        self.0
            .checked_add(delta)
            .map(Self)
            .ok_or(DomainError::InvalidTimestamp)
    }

    pub fn checked_sub_nanos(self, delta: i64) -> Result<Self, DomainError> {
        self.0
            .checked_sub(delta)
            .map(Self)
            .ok_or(DomainError::InvalidTimestamp)
    }

    /// Difference `self - earlier` in nanoseconds.
    pub fn checked_duration_since(self, earlier: Self) -> Result<i64, DomainError> {
        self.0
            .checked_sub(earlier.0)
            .ok_or(DomainError::InvalidTimestamp)
    }
}

#[cfg(test)]
mod tests {
    use super::Timestamp;
    use crate::error::DomainError;

    #[test]
    fn epoch_and_millis_conversions() {
        assert_eq!(Timestamp::UNIX_EPOCH.as_unix_nanos(), 0);
        assert_eq!(
            Timestamp::from_unix_millis(1_500).unwrap().as_unix_nanos(),
            1_500_000_000
        );
        assert_eq!(
            Timestamp::from_unix_nanos(1_500_999_999).as_unix_millis(),
            1_500
        );
        assert_eq!(Timestamp::from_unix_nanos(-1).as_unix_millis(), -1);
        assert_eq!(
            Timestamp::from_unix_millis(i64::MAX),
            Err(DomainError::InvalidTimestamp)
        );
    }

    #[test]
    fn parses_and_normalizes_utc_offsets() {
        assert_eq!(
            Timestamp::from_iso8601("1970-01-01T00:00:00Z")
                .unwrap()
                .as_unix_nanos(),
            0
        );
        assert_eq!(
            Timestamp::from_iso8601("1970-01-01T01:00:00+01:00")
                .unwrap()
                .as_unix_nanos(),
            0
        );
        assert_eq!(
            Timestamp::from_iso8601("1969-12-31T23:59:59.999999999Z")
                .unwrap()
                .as_unix_nanos(),
            -1
        );
        for input in ["", "1970-01-01", "1970-01-01T00:00:00", "not-a-time"] {
            assert_eq!(
                Timestamp::from_iso8601(input),
                Err(DomainError::InvalidTimestamp),
                "input {input:?}"
            );
        }
    }

    #[test]
    fn formats_as_utc_and_round_trips() {
        let timestamp = Timestamp::from_unix_nanos(1_700_000_000_123_456_789);
        let text = timestamp.to_iso8601().unwrap();
        assert!(text.ends_with('Z'));
        assert_eq!(Timestamp::from_iso8601(&text).unwrap(), timestamp);

        let epoch = Timestamp::UNIX_EPOCH.to_iso8601().unwrap();
        assert!(epoch.ends_with('Z'));
        assert_eq!(
            Timestamp::from_iso8601(&epoch).unwrap(),
            Timestamp::UNIX_EPOCH
        );
    }

    #[test]
    fn checked_time_arithmetic_reports_overflow() {
        assert_eq!(
            Timestamp::UNIX_EPOCH
                .checked_add_nanos(5)
                .unwrap()
                .as_unix_nanos(),
            5
        );
        assert_eq!(
            Timestamp::from_unix_nanos(5)
                .checked_sub_nanos(5)
                .unwrap()
                .as_unix_nanos(),
            0
        );
        assert_eq!(
            Timestamp::from_unix_nanos(5)
                .checked_duration_since(Timestamp::UNIX_EPOCH)
                .unwrap(),
            5
        );
        assert_eq!(
            Timestamp::from_unix_nanos(i64::MAX).checked_add_nanos(1),
            Err(DomainError::InvalidTimestamp)
        );
        assert_eq!(
            Timestamp::from_unix_nanos(i64::MIN).checked_sub_nanos(1),
            Err(DomainError::InvalidTimestamp)
        );
        assert_eq!(
            Timestamp::from_unix_nanos(i64::MIN)
                .checked_duration_since(Timestamp::from_unix_nanos(i64::MAX)),
            Err(DomainError::InvalidTimestamp)
        );
    }

    #[test]
    fn serde_uses_unix_nanoseconds() {
        let timestamp = Timestamp::from_unix_nanos(123);
        assert_eq!(serde_json::to_string(&timestamp).unwrap(), "123");
        let decoded: Timestamp = serde_json::from_str("123").unwrap();
        assert_eq!(decoded, timestamp);
    }
}
