//! Typed identifiers with strict parsing, canonical formatting, and serde.
//!
//! Numeric identifiers are opaque in this layer; shard encodings are assigned
//! by the owning service and are not guessed here. Account, journal, and hold
//! identifiers are UUIDv7. Ordinary order and trade identifiers are `u64` as
//! required by the architecture.

use std::fmt;
use std::str::FromStr;

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use uuid::Uuid;

use crate::error::DomainError;

macro_rules! numeric_id {
    ($name:ident, $inner:ty, $parse:ident) => {
        #[derive(
            Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize,
        )]
        #[serde(transparent)]
        pub struct $name($inner);

        impl $name {
            pub const fn new(value: $inner) -> Self {
                Self(value)
            }

            pub const fn get(self) -> $inner {
                self.0
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                write!(formatter, "{}", self.0)
            }
        }

        impl FromStr for $name {
            type Err = DomainError;

            fn from_str(input: &str) -> Result<Self, Self::Err> {
                $parse(input, stringify!($name)).map(Self::new)
            }
        }
    };
}

numeric_id!(AssetId, u32, parse_u32_identifier);
numeric_id!(SymbolId, u32, parse_u32_identifier);
numeric_id!(OrderId, u64, parse_u64_identifier);
numeric_id!(TradeId, u64, parse_u64_identifier);

fn parse_u32_identifier(input: &str, label: &'static str) -> Result<u32, DomainError> {
    let value = parse_digits(input, label)?;
    u32::try_from(value).map_err(|_| DomainError::InvalidId(label))
}

fn parse_u64_identifier(input: &str, label: &'static str) -> Result<u64, DomainError> {
    parse_digits(input, label)
}

fn parse_digits(input: &str, label: &'static str) -> Result<u64, DomainError> {
    if input.is_empty() || !input.as_bytes().iter().all(u8::is_ascii_digit) {
        return Err(DomainError::InvalidId(label));
    }
    input
        .parse::<u64>()
        .map_err(|_| DomainError::InvalidId(label))
}

macro_rules! uuid_id {
    ($name:ident, $label:literal) => {
        #[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
        pub struct $name(Uuid);

        impl $name {
            pub fn new(value: Uuid) -> Result<Self, DomainError> {
                ensure_uuid_v7(value)?;
                Ok(Self(value))
            }

            /// Generate a time-ordered UUIDv7. ID generation belongs at the
            /// edge or owning service, never inside deterministic matching.
            pub fn generate() -> Self {
                Self(Uuid::now_v7())
            }

            pub const fn get(self) -> Uuid {
                self.0
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                write!(formatter, "{}", self.0.hyphenated())
            }
        }

        impl FromStr for $name {
            type Err = DomainError;

            fn from_str(input: &str) -> Result<Self, Self::Err> {
                let value = Uuid::parse_str(input).map_err(|_| DomainError::InvalidId($label))?;
                Self::new(value)
            }
        }

        impl Serialize for $name {
            fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
            where
                S: Serializer,
            {
                serializer.serialize_str(&self.to_string())
            }
        }

        impl<'de> Deserialize<'de> for $name {
            fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
            where
                D: Deserializer<'de>,
            {
                let value = String::deserialize(deserializer)?;
                value.parse().map_err(serde::de::Error::custom)
            }
        }
    };
}

uuid_id!(AccountId, "account ID");
uuid_id!(JournalId, "journal ID");
uuid_id!(HoldId, "hold ID");

fn ensure_uuid_v7(value: Uuid) -> Result<(), DomainError> {
    if value.get_version_num() == 7 {
        Ok(())
    } else {
        Err(DomainError::InvalidUuidVersion(value.get_version_num()))
    }
}

#[cfg(test)]
mod tests {
    use std::str::FromStr;

    use uuid::Uuid;

    use super::{AccountId, AssetId, HoldId, JournalId, OrderId, SymbolId, TradeId};
    use crate::error::DomainError;

    #[test]
    fn numeric_ids_parse_and_format_canonically() {
        assert_eq!(AssetId::from_str("42").unwrap().get(), 42);
        assert_eq!(AssetId::from_str("00042").unwrap().to_string(), "42");
        assert_eq!(SymbolId::from_str("0").unwrap().get(), 0);
        assert_eq!(
            OrderId::from_str("18446744073709551615").unwrap().get(),
            u64::MAX
        );
        assert_eq!(TradeId::from_str("1").unwrap().to_string(), "1");
    }

    #[test]
    fn numeric_ids_reject_ambiguous_input() {
        for input in ["", "+1", "-1", " 1", "1 ", "1.0", "0x10", "1e3"] {
            assert_eq!(
                AssetId::from_str(input),
                Err(DomainError::InvalidId("AssetId")),
                "input {input:?}"
            );
        }
        assert_eq!(
            AssetId::from_str("4294967296"),
            Err(DomainError::InvalidId("AssetId"))
        );
        assert_eq!(
            OrderId::from_str("18446744073709551616"),
            Err(DomainError::InvalidId("OrderId"))
        );
    }

    #[test]
    fn uuid_ids_require_version_seven() {
        let v7 = Uuid::now_v7();
        let account = AccountId::new(v7).unwrap();
        assert_eq!(account.get(), v7);
        assert_eq!(account.to_string(), v7.hyphenated().to_string());
        assert_eq!(AccountId::from_str(&account.to_string()).unwrap(), account);

        let v4 = Uuid::new_v4();
        assert_eq!(AccountId::new(v4), Err(DomainError::InvalidUuidVersion(4)));
        assert_eq!(
            AccountId::from_str(&v4.to_string()),
            Err(DomainError::InvalidUuidVersion(4))
        );
        assert_eq!(
            AccountId::from_str("not-a-uuid"),
            Err(DomainError::InvalidId("account ID"))
        );
        assert_eq!(
            JournalId::from_str(&Uuid::nil().to_string()),
            Err(DomainError::InvalidUuidVersion(0))
        );
    }

    #[test]
    fn uuid_generation_produces_v7() {
        assert_eq!(AccountId::generate().get().get_version_num(), 7);
        assert_eq!(JournalId::generate().get().get_version_num(), 7);
        assert_eq!(HoldId::generate().get().get_version_num(), 7);
    }

    #[test]
    fn serde_round_trips_ids() {
        let account = AccountId::generate();
        let encoded = serde_json::to_string(&account).unwrap();
        assert_eq!(encoded, format!("\"{account}\""));
        let decoded: AccountId = serde_json::from_str(&encoded).unwrap();
        assert_eq!(decoded, account);

        let order = OrderId::new(7);
        let encoded = serde_json::to_string(&order).unwrap();
        assert_eq!(encoded, "7");
        let decoded: OrderId = serde_json::from_str(&encoded).unwrap();
        assert_eq!(decoded, order);
    }
}
