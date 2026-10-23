// Package refdata defines the versioned NEXORA reference-data snapshot
// contract and a consumer SDK for future Go services.
//
// The snapshot document is canonical JSON. It contains only reference
// configuration: assets, blockchain networks, trading instruments, fee
// schedules, margin tiers, and presentation policy. It never contains balances,
// positions, orders, trades, or executable blockchain material.
package refdata

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the only snapshot schema accepted by this package.
const SchemaVersion = "nexora.reference-data.v1"

// MaxNotionalExponent is the largest notional bound accepted for an
// instrument: 10^30 atomic units, matching the architecture's Amount bound.
const MaxNotionalExponent = 30

// MaxNotionalAmount returns 10^30 atomic units.
func MaxNotionalAmount() Amount {
	return MustAmount("1" + strings.Repeat("0", MaxNotionalExponent))
}

// Document is one complete, immutable reference-data snapshot.
type Document struct {
	SchemaVersion        string               `json:"schema_version"`
	Assets               []Asset              `json:"assets"`
	Networks             []Network            `json:"networks"`
	AssetNetworks        []AssetNetwork       `json:"asset_networks"`
	Instruments          []Instrument         `json:"instruments"`
	FeeSchedules         []FeeSchedule        `json:"fee_schedules"`
	MarginTierSets       []MarginTierSet      `json:"margin_tier_sets"`
	PresentationPolicies []PresentationPolicy `json:"presentation_policies"`
}

// Asset is a cryptocurrency or approved stablecoin.
type Asset struct {
	AssetID         int32   `json:"asset_id"`
	Symbol          string  `json:"symbol"`
	DisplayName     string  `json:"display_name"`
	Decimals        int16   `json:"decimals"`
	Kind            string  `json:"kind"`
	Status          string  `json:"status"`
	TradingEnabled  bool    `json:"trading_enabled"`
	TransferEnabled bool    `json:"transfer_enabled"`
	DepositEnabled  bool    `json:"deposit_enabled"`
	WithdrawEnabled bool    `json:"withdrawal_enabled"`
	MinDeposit      *Amount `json:"min_deposit"`
	MaxDeposit      *Amount `json:"max_deposit"`
	MinWithdrawal   *Amount `json:"min_withdrawal"`
	MaxWithdrawal   *Amount `json:"max_withdrawal"`
	WithdrawalFee   Amount  `json:"withdrawal_fee"`
}

// Network is a blockchain network independent of a custody provider.
type Network struct {
	NetworkID       string  `json:"network_id"`
	DisplayName     string  `json:"display_name"`
	ChainID         *string `json:"chain_id"`
	NativeAssetID   *int32  `json:"native_asset_id"`
	AddressFormat   string  `json:"address_format"`
	MemoRequired    bool    `json:"memo_required"`
	Confirmations   int16   `json:"confirmations"`
	Status          string  `json:"status"`
	DepositEnabled  bool    `json:"deposit_enabled"`
	WithdrawEnabled bool    `json:"withdrawal_enabled"`
	MinDeposit      Amount  `json:"min_deposit"`
	MinWithdrawal   Amount  `json:"min_withdrawal"`
	WithdrawalFee   Amount  `json:"withdrawal_fee"`
}

// AssetNetwork maps one asset onto one network. Two networks for the same asset
// are always distinct transfer paths.
type AssetNetwork struct {
	AssetID         int32   `json:"asset_id"`
	NetworkID       string  `json:"network_id"`
	ContractAddress *string `json:"contract_address"`
	Decimals        int16   `json:"decimals"`
	DepositEnabled  bool    `json:"deposit_enabled"`
	WithdrawEnabled bool    `json:"withdrawal_enabled"`
	MinDeposit      Amount  `json:"min_deposit"`
	MinWithdrawal   Amount  `json:"min_withdrawal"`
	WithdrawalFee   Amount  `json:"withdrawal_fee"`
	MemoRequired    bool    `json:"memo_required"`
	Confirmations   int16   `json:"confirmations"`
	Status          string  `json:"status"`
}

// Instrument follows nexora-types::Instrument serde exactly for the core
// fields, so the Rust SDK can validate it with the B02 domain primitives.
type Instrument struct {
	SymbolID      int32         `json:"symbol_id"`
	Symbol        string        `json:"symbol"`
	Kind          string        `json:"kind"`
	Base          int32         `json:"base"`
	Quote         int32         `json:"quote"`
	TickSize      int64         `json:"tick_size"`
	LotSize       int64         `json:"lot_size"`
	PriceDecimals int16         `json:"price_decimals"`
	QtyDecimals   int16         `json:"qty_decimals"`
	MinQty        int64         `json:"min_qty"`
	MaxQty        int64         `json:"max_qty"`
	MinNotional   Amount        `json:"min_notional"`
	MaxNotional   Amount        `json:"max_notional"`
	PriceBandBps  int32         `json:"price_band_bps"`
	MakerFeeBps   int32         `json:"maker_fee_bps"`
	TakerFeeBps   int32         `json:"taker_fee_bps"`
	Contract      *ContractSpec `json:"contract"`
	Status        string        `json:"status"`

	AllowedOrderTypes        []string `json:"allowed_order_types"`
	AllowedTimeInForce       []string `json:"allowed_time_in_force"`
	ListedAt                 string   `json:"listed_at"`
	DelistedAt               *string  `json:"delisted_at"`
	FeeScheduleID            string   `json:"fee_schedule_id"`
	MarginTierSetID          *string  `json:"margin_tier_set_id"`
	RiskLimitRef             *string  `json:"risk_limit_ref"`
	PriceSourcePolicyRef     *string  `json:"price_source_policy_ref"`
	MarginModeCapabilities   []string `json:"margin_mode_capabilities"`
	PositionModeCapabilities []string `json:"position_mode_capabilities"`
}

// ContractSpec is a linear USDT-M contract specification. No execution logic
// belongs to reference data.
type ContractSpec struct {
	ContractSize        Multiplier   `json:"contract_size"`
	SettlementAsset     int32        `json:"settlement_asset"`
	MaxLeverage         int32        `json:"max_leverage"`
	MarginTiers         []MarginTier `json:"margin_tiers"`
	FundingIntervalSecs int32        `json:"funding_interval_secs"`
	FundingCapBps       int32        `json:"funding_cap_bps"`
	LiquidationFeeBps   int32        `json:"liquidation_fee_bps"`
	ADLEnabled          bool         `json:"adl_enabled"`
}

// Multiplier is an exact positive rational.
type Multiplier struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// MarginTier is one validated notional bracket.
type MarginTier struct {
	NotionalFloor     Amount `json:"notional_floor"`
	NotionalCap       Amount `json:"notional_cap"`
	MaxLeverage       int32  `json:"max_leverage"`
	MMRBps            int32  `json:"mmr_bps"`
	MaintenanceAmount Amount `json:"maintenance_amount"`
}

// FeeSchedule is a versioned maker/taker schedule with volume tiers.
type FeeSchedule struct {
	ScheduleID      string    `json:"schedule_id"`
	Name            string    `json:"name"`
	MarketType      string    `json:"market_type"`
	IsDefault       bool      `json:"is_default"`
	AccountCategory string    `json:"account_category"`
	EffectiveAt     string    `json:"effective_at"`
	Status          string    `json:"status"`
	RebateEnabled   bool      `json:"rebate_enabled"`
	Tiers           []FeeTier `json:"tiers"`
}

// FeeTier is one volume band. MaxVolume is nil only for the final open band.
type FeeTier struct {
	TierIndex int32   `json:"tier_index"`
	MinVolume Amount  `json:"min_volume"`
	MaxVolume *Amount `json:"max_volume"`
	MakerBps  int32   `json:"maker_bps"`
	TakerBps  int32   `json:"taker_bps"`
}

// MarginTierSet is a reusable, versioned margin bracket set.
type MarginTierSet struct {
	SetID       string       `json:"set_id"`
	Name        string       `json:"name"`
	MarketType  string       `json:"market_type"`
	EffectiveAt string       `json:"effective_at"`
	Status      string       `json:"status"`
	MaxLeverage int32        `json:"max_leverage"`
	Tiers       []MarginTier `json:"tiers"`
}

// PresentationPolicy is display-only configuration. It must never be an input
// to matching, ledger, settlement, risk, funding, liquidation, or ADL.
type PresentationPolicy struct {
	PolicyID             string   `json:"policy_id"`
	Symbol               string   `json:"symbol"`
	SourcePriority       []string `json:"source_priority"`
	FallbackPriority     []string `json:"fallback_priority"`
	FreshnessMS          int32    `json:"freshness_ms"`
	StalenessMS          int32    `json:"staleness_ms"`
	MaxDeviationBps      int32    `json:"max_deviation_bps"`
	QuoteMethod          string   `json:"quote_method"`
	MaxQuoteOffsetBps    int32    `json:"max_quote_offset_bps"`
	ShowSyntheticVolume  bool     `json:"show_synthetic_volume"`
	SyntheticVolumeLabel string   `json:"synthetic_volume_label"`
	AuthoritativeForRisk bool     `json:"authoritative_for_risk"`
	Status               string   `json:"status"`
}

// Normalize returns a deep copy with deterministic ordering and non-nil
// collections. Canonical serialization always uses the normalized form.
func (d Document) Normalize() Document {
	out := d
	out.SchemaVersion = strings.TrimSpace(out.SchemaVersion)
	if out.SchemaVersion == "" {
		out.SchemaVersion = SchemaVersion
	}
	out.Assets = append([]Asset(nil), out.Assets...)
	out.Networks = append([]Network(nil), out.Networks...)
	out.AssetNetworks = append([]AssetNetwork(nil), out.AssetNetworks...)
	out.Instruments = append([]Instrument(nil), out.Instruments...)
	out.FeeSchedules = append([]FeeSchedule(nil), out.FeeSchedules...)
	out.MarginTierSets = append([]MarginTierSet(nil), out.MarginTierSets...)
	out.PresentationPolicies = append([]PresentationPolicy(nil), out.PresentationPolicies...)

	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].AssetID < out.Assets[j].AssetID })
	sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].NetworkID < out.Networks[j].NetworkID })
	sort.Slice(out.AssetNetworks, func(i, j int) bool {
		if out.AssetNetworks[i].AssetID != out.AssetNetworks[j].AssetID {
			return out.AssetNetworks[i].AssetID < out.AssetNetworks[j].AssetID
		}
		return out.AssetNetworks[i].NetworkID < out.AssetNetworks[j].NetworkID
	})
	sort.Slice(out.Instruments, func(i, j int) bool { return out.Instruments[i].SymbolID < out.Instruments[j].SymbolID })
	for index := range out.Instruments {
		instrument := &out.Instruments[index]
		instrument.AllowedOrderTypes = sortedUnique(instrument.AllowedOrderTypes)
		instrument.AllowedTimeInForce = sortedUnique(instrument.AllowedTimeInForce)
		instrument.MarginModeCapabilities = sortedUnique(instrument.MarginModeCapabilities)
		instrument.PositionModeCapabilities = sortedUnique(instrument.PositionModeCapabilities)
		if instrument.Contract != nil {
			instrument.Contract.MarginTiers = append([]MarginTier(nil), instrument.Contract.MarginTiers...)
			sort.Slice(instrument.Contract.MarginTiers, func(a, b int) bool {
				return instrument.Contract.MarginTiers[a].NotionalFloor.Cmp(instrument.Contract.MarginTiers[b].NotionalFloor) < 0
			})
		}
	}
	sort.Slice(out.FeeSchedules, func(i, j int) bool { return out.FeeSchedules[i].ScheduleID < out.FeeSchedules[j].ScheduleID })
	for index := range out.FeeSchedules {
		schedule := &out.FeeSchedules[index]
		schedule.Tiers = append([]FeeTier(nil), schedule.Tiers...)
		sort.Slice(schedule.Tiers, func(a, b int) bool { return schedule.Tiers[a].TierIndex < schedule.Tiers[b].TierIndex })
	}
	sort.Slice(out.MarginTierSets, func(i, j int) bool { return out.MarginTierSets[i].SetID < out.MarginTierSets[j].SetID })
	for index := range out.MarginTierSets {
		set := &out.MarginTierSets[index]
		set.Tiers = append([]MarginTier(nil), set.Tiers...)
		sort.Slice(set.Tiers, func(a, b int) bool {
			return set.Tiers[a].NotionalFloor.Cmp(set.Tiers[b].NotionalFloor) < 0
		})
	}
	sort.Slice(out.PresentationPolicies, func(i, j int) bool {
		return out.PresentationPolicies[i].PolicyID < out.PresentationPolicies[j].PolicyID
	})
	for index := range out.PresentationPolicies {
		policy := &out.PresentationPolicies[index]
		policy.SourcePriority = append([]string(nil), policy.SourcePriority...)
		policy.FallbackPriority = append([]string(nil), policy.FallbackPriority...)
	}
	return out
}

// IsZero reports whether the document has no configuration entries.
func (d Document) IsZero() bool {
	return len(d.Assets) == 0 && len(d.Networks) == 0 && len(d.AssetNetworks) == 0 &&
		len(d.Instruments) == 0 && len(d.FeeSchedules) == 0 &&
		len(d.MarginTierSets) == 0 && len(d.PresentationPolicies) == 0
}

// ContentHash returns the lowercase hex SHA-256 of canonical bytes.
func ContentHash(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// VerifyHash checks a lowercase or uppercase hex SHA-256 value.
func VerifyHash(canonical []byte, expected string) error {
	normalized := strings.ToLower(strings.TrimSpace(expected))
	if len(normalized) != 64 {
		return fmt.Errorf("invalid content hash length")
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return fmt.Errorf("invalid content hash: %w", err)
	}
	actual := ContentHash(canonical)
	if actual != normalized {
		return ErrHashMismatch
	}
	return nil
}

var (
	// ErrHashMismatch indicates snapshot bytes do not match the declared hash.
	ErrHashMismatch = errors.New("reference-data content hash mismatch")
	// ErrSchema indicates an unsupported document schema version.
	ErrSchema = errors.New("unsupported reference-data schema version")
	// ErrStaleVersion indicates a non-rollback version regression.
	ErrStaleVersion = errors.New("reference-data version is older than the active snapshot")
)

// Snapshot is a verified immutable document plus its publication metadata.
type Snapshot struct {
	Version       int64
	ContentHash   string
	SchemaVersion string
	EffectiveAt   time.Time
	ShardID       string
	Rollback      bool
	Document      Document
	Canonical     []byte
}

// Age returns the age of the snapshot relative to now.
func (s Snapshot) Age(now time.Time) time.Duration {
	if s.EffectiveAt.IsZero() {
		return 0
	}
	return now.Sub(s.EffectiveAt)
}

// Stale reports whether the snapshot exceeded maxAge.
func (s Snapshot) Stale(now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 || s.EffectiveAt.IsZero() {
		return false
	}
	return now.Sub(s.EffectiveAt) > maxAge
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
