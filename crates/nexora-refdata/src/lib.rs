//! Verified, version-aware consumption of NEXORA reference-data snapshots.
//!
//! This crate is transport-agnostic. A future Rust service obtains the raw
//! snapshot bytes and integrity metadata from the reference-data service (HTTP
//! or gRPC), calls [`verify_and_parse`], and atomically installs the result in a
//! [`Consumer`]. Published snapshots are complete documents: a consumer never
//! merges individual assets or instruments from different versions.
//!
//! The verifier hashes the exact received bytes. It never re-serializes the
//! document before hashing, so the Go producer and Rust consumer agree on the
//! integrity value without relying on cross-language JSON canonicalization.
//! Core instrument and margin-tier semantics are independently re-validated
//! with the B02 [`nexora_types`] primitives.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, RwLock};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use thiserror::Error;

use nexora_types::{
    Amount, AssetId, Bps, ContractSpec, FeeSchedule as FeeSchedulePrimitive, Instrument,
    InstrumentKind, InstrumentStatus, MarginTier, Qty, SymbolId,
};

/// The only snapshot schema version accepted by this crate.
pub const SCHEMA_VERSION: &str = "nexora.reference-data.v1";

/// Verification, schema, or semantic failure.
#[derive(Debug, Error)]
pub enum RefDataError {
    /// The declared hash is not a 64-character lowercase or uppercase hex value.
    #[error("invalid reference-data content hash")]
    InvalidHash,
    /// Received bytes do not match the declared SHA-256 hash.
    #[error("reference-data content hash mismatch")]
    HashMismatch,
    /// JSON decoding failed.
    #[error("decode reference-data snapshot: {0}")]
    Json(#[from] serde_json::Error),
    /// The document declares an unsupported schema.
    #[error("unsupported reference-data schema version: {0}")]
    Schema(String),
    /// Semantic validation failed.
    #[error("reference-data validation failed: {0}")]
    Validation(String),
    /// A non-rollback version regression was attempted.
    #[error("reference-data version {candidate} is older than active version {current}")]
    StaleVersion {
        /// Active version.
        current: i64,
        /// Candidate version.
        candidate: i64,
    },
    /// A rollback was allowed by policy but the snapshot was not marked as one.
    #[error("reference-data rollback must be explicit")]
    RollbackNotExplicit,
    /// The same version arrived with different content.
    #[error("reference-data version {version} has conflicting content hashes")]
    VersionHashConflict {
        /// Conflicting version.
        version: i64,
    },
}

/// One asset in the snapshot.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Asset {
    pub asset_id: i32,
    pub symbol: String,
    pub display_name: String,
    pub decimals: i16,
    pub kind: String,
    pub status: String,
    pub trading_enabled: bool,
    pub transfer_enabled: bool,
    pub deposit_enabled: bool,
    pub withdrawal_enabled: bool,
    pub min_deposit: Option<i128>,
    pub max_deposit: Option<i128>,
    pub min_withdrawal: Option<i128>,
    pub max_withdrawal: Option<i128>,
    pub withdrawal_fee: i128,
}

/// One blockchain network.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Network {
    pub network_id: String,
    pub display_name: String,
    pub chain_id: Option<String>,
    pub native_asset_id: Option<i32>,
    pub address_format: String,
    pub memo_required: bool,
    pub confirmations: i16,
    pub status: String,
    pub deposit_enabled: bool,
    pub withdrawal_enabled: bool,
    pub min_deposit: i128,
    pub min_withdrawal: i128,
    pub withdrawal_fee: i128,
}

/// One asset-to-network transfer path.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AssetNetwork {
    pub asset_id: i32,
    pub network_id: String,
    pub contract_address: Option<String>,
    pub decimals: i16,
    pub deposit_enabled: bool,
    pub withdrawal_enabled: bool,
    pub min_deposit: i128,
    pub min_withdrawal: i128,
    pub withdrawal_fee: i128,
    pub memo_required: bool,
    pub confirmations: i16,
    pub status: String,
}

/// One instrument plus B04-only references. The core fields are flattened so
/// the B02 [`Instrument`] validator sees exactly the frozen trading contract.
#[derive(Debug, Clone)]
pub struct InstrumentRecord {
    pub core: Instrument,
    pub price_decimals: i16,
    pub qty_decimals: i16,
    pub fee_schedule_id: String,
    pub margin_tier_set_id: Option<String>,
    pub allowed_order_types: Vec<String>,
    pub allowed_time_in_force: Vec<String>,
    pub listed_at: String,
    pub delisted_at: Option<String>,
    pub risk_limit_ref: Option<String>,
    pub price_source_policy_ref: Option<String>,
    pub margin_mode_capabilities: Vec<String>,
    pub position_mode_capabilities: Vec<String>,
}

// Deserialize the flattened instrument without serde's buffered flatten
// representation, which cannot carry i128 amounts exactly.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct InstrumentWire {
    symbol_id: SymbolId,
    symbol: String,
    kind: InstrumentKind,
    base: AssetId,
    quote: AssetId,
    tick_size: i64,
    lot_size: i64,
    min_qty: Qty,
    max_qty: Qty,
    min_notional: Amount,
    max_notional: Amount,
    price_band_bps: i32,
    maker_fee_bps: Bps,
    taker_fee_bps: Bps,
    contract: Option<ContractSpec>,
    status: InstrumentStatus,
    price_decimals: i16,
    qty_decimals: i16,
    fee_schedule_id: String,
    margin_tier_set_id: Option<String>,
    allowed_order_types: Vec<String>,
    allowed_time_in_force: Vec<String>,
    listed_at: String,
    delisted_at: Option<String>,
    risk_limit_ref: Option<String>,
    price_source_policy_ref: Option<String>,
    margin_mode_capabilities: Vec<String>,
    position_mode_capabilities: Vec<String>,
}

impl<'de> Deserialize<'de> for InstrumentRecord {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let wire = InstrumentWire::deserialize(deserializer)?;
        Ok(Self {
            core: Instrument {
                symbol_id: wire.symbol_id,
                symbol: wire.symbol,
                kind: wire.kind,
                base: wire.base,
                quote: wire.quote,
                tick_size: wire.tick_size,
                lot_size: wire.lot_size,
                min_qty: wire.min_qty,
                max_qty: wire.max_qty,
                min_notional: wire.min_notional,
                max_notional: wire.max_notional,
                price_band_bps: wire.price_band_bps,
                maker_fee_bps: wire.maker_fee_bps,
                taker_fee_bps: wire.taker_fee_bps,
                contract: wire.contract,
                status: wire.status,
            },
            price_decimals: wire.price_decimals,
            qty_decimals: wire.qty_decimals,
            fee_schedule_id: wire.fee_schedule_id,
            margin_tier_set_id: wire.margin_tier_set_id,
            allowed_order_types: wire.allowed_order_types,
            allowed_time_in_force: wire.allowed_time_in_force,
            listed_at: wire.listed_at,
            delisted_at: wire.delisted_at,
            risk_limit_ref: wire.risk_limit_ref,
            price_source_policy_ref: wire.price_source_policy_ref,
            margin_mode_capabilities: wire.margin_mode_capabilities,
            position_mode_capabilities: wire.position_mode_capabilities,
        })
    }
}

/// One volume-based fee tier.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FeeTier {
    pub tier_index: i32,
    pub min_volume: i128,
    pub max_volume: Option<i128>,
    pub maker_bps: i32,
    pub taker_bps: i32,
}

/// One versioned maker/taker schedule.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FeeSchedule {
    pub schedule_id: String,
    pub name: String,
    pub market_type: String,
    pub is_default: bool,
    pub account_category: String,
    pub effective_at: String,
    pub status: String,
    pub rebate_enabled: bool,
    pub tiers: Vec<FeeTier>,
}

/// One reusable margin tier set.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MarginTierSet {
    pub set_id: String,
    pub name: String,
    pub market_type: String,
    pub effective_at: String,
    pub status: String,
    pub max_leverage: i32,
    pub tiers: Vec<MarginTier>,
}

/// Display-only presentation policy. Never an input to financial systems.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PresentationPolicy {
    pub policy_id: String,
    pub symbol: String,
    pub source_priority: Vec<String>,
    pub fallback_priority: Vec<String>,
    pub freshness_ms: i32,
    pub staleness_ms: i32,
    pub max_deviation_bps: i32,
    pub quote_method: String,
    pub max_quote_offset_bps: i32,
    pub show_synthetic_volume: bool,
    pub synthetic_volume_label: String,
    pub authoritative_for_risk: bool,
    pub status: String,
}

/// The complete immutable reference-data document.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotDocument {
    pub schema_version: String,
    pub assets: Vec<Asset>,
    pub networks: Vec<Network>,
    pub asset_networks: Vec<AssetNetwork>,
    pub instruments: Vec<InstrumentRecord>,
    pub fee_schedules: Vec<FeeSchedule>,
    pub margin_tier_sets: Vec<MarginTierSet>,
    pub presentation_policies: Vec<PresentationPolicy>,
}

/// A verified snapshot with exact canonical bytes and publication metadata.
#[derive(Debug, Clone)]
pub struct Snapshot {
    version: i64,
    content_hash: String,
    effective_at_unix: i64,
    shard_id: String,
    rollback: bool,
    document: Arc<SnapshotDocument>,
    canonical: Arc<[u8]>,
}

impl Snapshot {
    /// Published version, monotonically increasing except for an explicit
    /// rollback.
    pub fn version(&self) -> i64 {
        self.version
    }

    /// Lowercase SHA-256 of the exact canonical bytes.
    pub fn content_hash(&self) -> &str {
        &self.content_hash
    }

    /// Effective time in Unix seconds.
    pub fn effective_at_unix(&self) -> i64 {
        self.effective_at_unix
    }

    /// Logical shard that activated this snapshot.
    pub fn shard_id(&self) -> &str {
        &self.shard_id
    }

    /// True when this snapshot was activated as an explicit rollback.
    pub fn is_rollback(&self) -> bool {
        self.rollback
    }

    /// Parsed immutable document.
    pub fn document(&self) -> &SnapshotDocument {
        &self.document
    }

    /// Exact bytes covered by the content hash.
    pub fn canonical_bytes(&self) -> &[u8] {
        &self.canonical
    }

    /// Mark the snapshot as an explicit rollback. The reference-data service
    /// derives this from the activation record; transports that carry rollback
    /// intent separately can set it here before installing the snapshot.
    pub fn mark_rollback(mut self, rollback: bool) -> Self {
        self.rollback = rollback;
        self
    }
}

/// Verify the hash, decode, and fully validate a received snapshot.
#[allow(clippy::too_many_arguments)]
pub fn verify_and_parse(
    canonical: &[u8],
    expected_hash: &str,
    version: i64,
    effective_at_unix: i64,
    shard_id: &str,
    rollback: bool,
) -> Result<Snapshot, RefDataError> {
    if version <= 0 {
        return Err(RefDataError::Validation(
            "snapshot version must be positive".to_string(),
        ));
    }
    let expected = normalize_hash(expected_hash)?;
    let actual = hex_lower(Sha256::digest(canonical).as_slice());
    if actual != expected {
        return Err(RefDataError::HashMismatch);
    }
    let document: SnapshotDocument = serde_json::from_slice(canonical)?;
    validate_document(&document)?;
    Ok(Snapshot {
        version,
        content_hash: actual,
        effective_at_unix,
        shard_id: shard_id.to_string(),
        rollback,
        document: Arc::new(document),
        canonical: Arc::from(canonical),
    })
}

/// Validate the complete document. Core instrument rules are delegated to the
/// B02 primitives so B04 cannot drift from the trading domain contract.
pub fn validate_document(document: &SnapshotDocument) -> Result<(), RefDataError> {
    if document.schema_version != SCHEMA_VERSION {
        return Err(RefDataError::Schema(document.schema_version.clone()));
    }
    let mut asset_symbols = BTreeSet::new();
    let mut assets = BTreeMap::new();
    for asset in &document.assets {
        if asset.asset_id <= 0 {
            return invalid(format!("asset {} has a non-positive id", asset.symbol));
        }
        if !(0..=18).contains(&asset.decimals) {
            return invalid(format!("asset {} has invalid decimals", asset.symbol));
        }
        if !matches!(asset.kind.as_str(), "crypto" | "stablecoin") {
            return invalid(format!("asset {} has an unsupported kind", asset.symbol));
        }
        if !asset_symbols.insert(asset.symbol.clone()) {
            return invalid(format!("duplicate asset symbol {}", asset.symbol));
        }
        if assets.insert(asset.asset_id, asset).is_some() {
            return invalid(format!("duplicate asset id {}", asset.asset_id));
        }
    }

    let mut networks = BTreeSet::new();
    for network in &document.networks {
        if !networks.insert(network.network_id.clone()) {
            return invalid(format!("duplicate network {}", network.network_id));
        }
        if let Some(native) = network.native_asset_id
            && !assets.contains_key(&native)
        {
            return invalid(format!(
                "network {} references unknown native asset {}",
                network.network_id, native
            ));
        }
        if network.address_format == "evm_hex" && network.chain_id.is_none() {
            return invalid(format!(
                "EVM network {} is missing chain_id",
                network.network_id
            ));
        }
        ensure_non_negative(network.min_deposit, "network min_deposit")?;
        ensure_non_negative(network.min_withdrawal, "network min_withdrawal")?;
        ensure_non_negative(network.withdrawal_fee, "network withdrawal_fee")?;
    }

    let mut mappings = BTreeSet::new();
    for mapping in &document.asset_networks {
        if !mappings.insert((mapping.asset_id, mapping.network_id.clone())) {
            return invalid("duplicate asset/network mapping".to_string());
        }
        let asset = assets.get(&mapping.asset_id).ok_or_else(|| {
            validation_error(format!("unknown mapping asset {}", mapping.asset_id))
        })?;
        if !networks.contains(&mapping.network_id) {
            return invalid(format!("unknown mapping network {}", mapping.network_id));
        }
        if mapping.decimals < 0 || mapping.decimals > asset.decimals {
            return invalid("mapping decimals exceed asset decimals".to_string());
        }
        ensure_non_negative(mapping.min_deposit, "mapping min_deposit")?;
        ensure_non_negative(mapping.min_withdrawal, "mapping min_withdrawal")?;
        ensure_non_negative(mapping.withdrawal_fee, "mapping withdrawal_fee")?;
    }

    let mut schedules = BTreeMap::new();
    for schedule in &document.fee_schedules {
        if schedule.tiers.is_empty() {
            return invalid(format!(
                "fee schedule {} has no tiers",
                schedule.schedule_id
            ));
        }
        let mut previous: Option<&FeeTier> = None;
        for (index, tier) in schedule.tiers.iter().enumerate() {
            if tier.tier_index != index as i32 {
                return invalid(format!(
                    "fee schedule {} has non-contiguous tier indexes",
                    schedule.schedule_id
                ));
            }
            if tier.min_volume < 0 || tier.min_volume > 10_i128.pow(30) {
                return invalid("fee tier min_volume is out of range".to_string());
            }
            match (previous, tier.max_volume) {
                (None, _) if tier.min_volume != 0 => {
                    return invalid("first fee tier must start at zero".to_string());
                }
                (Some(prior), Some(max)) => {
                    if prior.max_volume != Some(tier.min_volume) {
                        return invalid("fee tiers must be contiguous".to_string());
                    }
                    if max <= tier.min_volume {
                        return invalid("fee tier max_volume must exceed min_volume".to_string());
                    }
                }
                (Some(_), None) => {}
                (None, Some(max)) if max <= 0 => {
                    return invalid("fee tier max_volume must be positive".to_string());
                }
                _ => {}
            }
            if index + 1 != schedule.tiers.len() && tier.max_volume.is_none() {
                return invalid("only the final fee tier may be open-ended".to_string());
            }
            if index + 1 == schedule.tiers.len() && tier.max_volume.is_some() {
                return invalid("the final fee tier must be open-ended".to_string());
            }
            if !(-100..=10_000).contains(&tier.maker_bps) {
                return invalid("maker_bps is out of range".to_string());
            }
            if tier.maker_bps < 0 && !schedule.rebate_enabled {
                return invalid("negative maker fees require rebate_enabled".to_string());
            }
            if !(0..=10_000).contains(&tier.taker_bps) {
                return invalid("taker_bps is out of range".to_string());
            }
            if let Some(prior) = previous
                && (tier.taker_bps > prior.taker_bps || tier.maker_bps > prior.maker_bps)
            {
                return invalid("fee rates must not increase with volume".to_string());
            }
            previous = Some(tier);
        }
        FeeSchedulePrimitive::new(
            nexora_types::Bps::new(schedule.tiers[0].maker_bps),
            nexora_types::Bps::new(schedule.tiers[0].taker_bps),
        )
        .validate()
        .map_err(|err| validation_error(err.to_string()))?;
        schedules.insert(schedule.schedule_id.clone(), schedule);
    }

    let mut margin_sets = BTreeMap::new();
    for set in &document.margin_tier_sets {
        if set.market_type != "perp" {
            return invalid("margin tier sets must use market_type perp".to_string());
        }
        if set.tiers.is_empty() {
            return invalid(format!("margin tier set {} has no tiers", set.set_id));
        }
        let mut previous: Option<&MarginTier> = None;
        for tier in &set.tiers {
            if previous.is_none() {
                if !tier.notional_floor.is_zero() || !tier.maintenance_amount.is_zero() {
                    return invalid("first margin tier must start at zero".to_string());
                }
            } else if let Some(prior) = previous {
                if tier.notional_floor != prior.notional_cap {
                    return invalid("margin tiers must be contiguous".to_string());
                }
                if tier.max_leverage > prior.max_leverage {
                    return invalid("max leverage must not increase across tiers".to_string());
                }
                if tier.mmr_bps.get() < prior.mmr_bps.get() {
                    return invalid("MMR must not decrease across tiers".to_string());
                }
                let delta = i128::from(tier.mmr_bps.get() - prior.mmr_bps.get());
                let product = tier
                    .notional_floor
                    .atomic()
                    .checked_mul(delta)
                    .ok_or_else(|| validation_error("margin tier derivation overflow"))?;
                if product % 10_000 != 0 {
                    return invalid("maintenance amount must be integral".to_string());
                }
                let expected = prior
                    .maintenance_amount
                    .checked_add(nexora_types::Amount::new(product / 10_000))
                    .map_err(|err| validation_error(err.to_string()))?;
                if tier.maintenance_amount != expected {
                    return invalid(
                        "maintenance amount must be derived from the prior tier".to_string(),
                    );
                }
            }
            if (tier.max_leverage as i32) > set.max_leverage {
                return invalid("tier leverage exceeds set maximum".to_string());
            }
            previous = Some(tier);
        }
        if set.max_leverage != i32::from(set.tiers[0].max_leverage) {
            return invalid("set max_leverage must equal the first tier".to_string());
        }
        margin_sets.insert(set.set_id.clone(), set);
    }

    let mut instrument_symbols = BTreeSet::new();
    for record in &document.instruments {
        let instrument = &record.core;
        instrument
            .validate()
            .map_err(|err| validation_error(err.to_string()))?;
        if !instrument_symbols.insert(instrument.symbol.clone()) {
            return invalid(format!("duplicate instrument symbol {}", instrument.symbol));
        }
        if !i32::try_from(instrument.base.get())
            .is_ok_and(|asset_id| assets.contains_key(&asset_id))
        {
            return invalid(format!(
                "instrument {} has an unknown base asset",
                instrument.symbol
            ));
        }
        if !i32::try_from(instrument.quote.get())
            .is_ok_and(|asset_id| assets.contains_key(&asset_id))
        {
            return invalid(format!(
                "instrument {} has an unknown quote asset",
                instrument.symbol
            ));
        }
        let schedule = schedules.get(&record.fee_schedule_id).ok_or_else(|| {
            validation_error(format!(
                "instrument {} references an unknown fee schedule",
                instrument.symbol
            ))
        })?;
        let expected_market = match instrument.kind {
            nexora_types::InstrumentKind::Spot => "spot",
            nexora_types::InstrumentKind::Perpetual => "perp",
        };
        if schedule.market_type != expected_market {
            return invalid(format!(
                "instrument {} fee schedule market does not match its kind",
                instrument.symbol
            ));
        }
        if instrument.maker_fee_bps.get() != schedule.tiers[0].maker_bps
            || instrument.taker_fee_bps.get() != schedule.tiers[0].taker_bps
        {
            return invalid(format!(
                "instrument {} fee fields do not match the base fee tier",
                instrument.symbol
            ));
        }
        if instrument.kind == nexora_types::InstrumentKind::Perpetual {
            let contract = instrument
                .contract
                .as_ref()
                .ok_or_else(|| validation_error("perpetual instrument lacks a contract"))?;
            let set_id = record.margin_tier_set_id.as_ref().ok_or_else(|| {
                validation_error(format!(
                    "instrument {} lacks a margin tier set",
                    instrument.symbol
                ))
            })?;
            let set = margin_sets.get(set_id).ok_or_else(|| {
                validation_error(format!(
                    "instrument {} references an unknown margin tier set",
                    instrument.symbol
                ))
            })?;
            if set.tiers != contract.margin_tiers {
                return invalid(format!(
                    "instrument {} contract margin tiers do not match set {}",
                    instrument.symbol, set_id
                ));
            }
            let expected_leverage = u16::try_from(set.max_leverage).map_err(|_| {
                validation_error("margin set max leverage is out of range".to_string())
            })?;
            if contract.max_leverage != expected_leverage {
                return invalid(format!(
                    "instrument {} contract max leverage differs from set {}",
                    instrument.symbol, set_id
                ));
            }
            if let Some(last) = set.tiers.last()
                && instrument.max_notional > last.notional_cap
            {
                return invalid(format!(
                    "instrument {} max notional exceeds the last margin tier",
                    instrument.symbol
                ));
            }
        }
    }

    let mut policy_ids = BTreeSet::new();
    for policy in &document.presentation_policies {
        if !policy_ids.insert(policy.policy_id.clone()) {
            return invalid(format!(
                "duplicate presentation policy {}",
                policy.policy_id
            ));
        }
        if !instrument_symbols.contains(&policy.symbol) {
            return invalid(format!(
                "presentation policy {} references unknown symbol {}",
                policy.policy_id, policy.symbol
            ));
        }
        if policy.source_priority.is_empty() {
            return invalid("presentation policy requires a source".to_string());
        }
        let primary: BTreeSet<_> = policy.source_priority.iter().collect();
        if primary.len() != policy.source_priority.len() {
            return invalid("duplicate primary market-data source".to_string());
        }
        if policy
            .fallback_priority
            .iter()
            .any(|source| primary.contains(source))
        {
            return invalid("fallback source duplicates a primary source".to_string());
        }
        if policy.freshness_ms < 100
            || policy.staleness_ms <= policy.freshness_ms
            || policy.staleness_ms > 300_000
        {
            return invalid("presentation freshness/staleness bounds are invalid".to_string());
        }
        if !(0..=1_000).contains(&policy.max_deviation_bps)
            || !(0..=policy.max_deviation_bps).contains(&policy.max_quote_offset_bps)
        {
            return invalid("presentation deviation bounds are invalid".to_string());
        }
        if policy.authoritative_for_risk {
            return invalid("presentation policy must never be authoritative for risk".to_string());
        }
        if policy.show_synthetic_volume {
            return invalid(
                "synthetic volume is not permitted; it must never be presented as executed volume"
                    .to_string(),
            );
        }
        if !policy.synthetic_volume_label.trim().is_empty() {
            return invalid(
                "synthetic_volume_label must be empty because synthetic volume is disabled"
                    .to_string(),
            );
        }
    }

    Ok(())
}

/// Thread-safe atomic snapshot holder.
///
/// Replacement takes the write lock only for a pointer swap. Readers clone an
/// `Arc` under a read lock, so no reader ever observes a partially replaced
/// document.
#[derive(Debug, Default)]
pub struct Consumer {
    current: RwLock<Option<Arc<Snapshot>>>,
}

impl Consumer {
    /// Construct an empty consumer.
    pub fn new() -> Self {
        Self {
            current: RwLock::new(None),
        }
    }

    /// Install a verified snapshot.
    ///
    /// A regression requires both `allow_rollback` and a snapshot explicitly
    /// marked as a rollback by the source service.
    pub fn apply(&self, snapshot: Snapshot, allow_rollback: bool) -> Result<(), RefDataError> {
        let mut guard = self
            .current
            .write()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if let Some(current) = guard.as_ref() {
            if snapshot.version == current.version {
                if snapshot.content_hash == current.content_hash {
                    return Ok(());
                }
                return Err(RefDataError::VersionHashConflict {
                    version: snapshot.version,
                });
            }
            if snapshot.version < current.version {
                if !allow_rollback {
                    return Err(RefDataError::StaleVersion {
                        current: current.version,
                        candidate: snapshot.version,
                    });
                }
                if !snapshot.rollback {
                    return Err(RefDataError::RollbackNotExplicit);
                }
            }
        }
        *guard = Some(Arc::new(snapshot));
        Ok(())
    }

    /// Current verified snapshot.
    pub fn current(&self) -> Option<Arc<Snapshot>> {
        self.current
            .read()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
            .clone()
    }

    /// Current version, if any.
    pub fn version(&self) -> Option<i64> {
        self.current().map(|snapshot| snapshot.version)
    }
}

fn invalid(message: String) -> Result<(), RefDataError> {
    Err(validation_error(message))
}

fn validation_error(message: impl Into<String>) -> RefDataError {
    RefDataError::Validation(message.into())
}

fn ensure_non_negative(value: i128, field: &str) -> Result<(), RefDataError> {
    if value < 0 || value > 10_i128.pow(30) {
        return invalid(format!("{field} is out of range"));
    }
    Ok(())
}

fn normalize_hash(expected: &str) -> Result<String, RefDataError> {
    let expected = expected.trim().to_ascii_lowercase();
    if expected.len() != 64 || !expected.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err(RefDataError::InvalidHash);
    }
    Ok(expected)
}

fn hex_lower(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        output.push(char::from(HEX[(byte >> 4) as usize]));
        output.push(char::from(HEX[(byte & 0x0f) as usize]));
    }
    output
}
