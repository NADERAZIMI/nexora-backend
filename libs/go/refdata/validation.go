package refdata

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Issue is one deterministic validation finding.
type Issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError aggregates all findings for one rejected document.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "reference-data validation failed"
	}
	first := e.Issues[0]
	return fmt.Sprintf("reference-data validation failed at %s: %s", first.Path, first.Message)
}

var (
	assetSymbolPattern      = regexp.MustCompile(`^[A-Z0-9]{2,16}$`)
	instrumentSymbolPattern = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)
	networkIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)
	configIDPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,63}$`)
	venuePattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)
	evmAddressPattern       = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	tronAddressPattern      = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)
	solanaAddressPattern    = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	digitsPattern           = regexp.MustCompile(`^[0-9]{1,20}$`)
)

var (
	validAssetKinds     = set("crypto", "stablecoin")
	validAssetStatuses  = set("active", "maintenance", "delisted")
	validNetworkStatus  = set("active", "maintenance", "disabled")
	validNetworkFormats = set("evm_hex", "bitcoin", "tron_base58", "solana_base58")
	validInstrumentKind = set("SPOT", "PERPETUAL")
	validInstrumentStat = set("ACTIVE", "REDUCE_ONLY", "HALTED", "DELISTED")
	validOrderTypes     = set("LIMIT", "MARKET", "STOP_MARKET", "STOP_LIMIT", "TAKE_PROFIT", "TRAILING_STOP", "CLOSE_POSITION")
	validTimeInForce    = set("GTC", "IOC", "FOK", "GTD", "DAY")
	validMarketTypes    = set("spot", "perp")
	validMarginModes    = set("cross", "isolated")
	validPositionModes  = set("one_way", "hedge")
	validQuoteMethods   = set("midpoint", "last", "index", "bounded_offset")
	validPolicyStatus   = set("active", "disabled")
	validScheduleStatus = set("draft", "active", "retired")
)

// Validate performs full semantic validation. It returns a stable, sorted list
// of issues and never panics on malformed configuration.
func (d Document) Validate() []Issue {
	doc := d.Normalize()
	maxAtomic := MaxAtomicAmount()
	var issues []Issue
	add := func(path, code, format string, args ...any) {
		issues = append(issues, Issue{Path: path, Code: code, Message: fmt.Sprintf(format, args...)})
	}

	if doc.SchemaVersion != SchemaVersion {
		add("schema_version", "REF_SCHEMA", "unsupported schema version %q", doc.SchemaVersion)
	}

	assets := make(map[int32]Asset, len(doc.Assets))
	assetSymbols := make(map[string]struct{}, len(doc.Assets))
	for index, asset := range doc.Assets {
		path := fmt.Sprintf("assets[%d]", index)
		if asset.AssetID <= 0 {
			add(path+".asset_id", "REF_RANGE", "asset_id must be positive")
		} else if _, exists := assets[asset.AssetID]; exists {
			add(path+".asset_id", "REF_DUPLICATE", "duplicate asset_id %d", asset.AssetID)
		} else {
			assets[asset.AssetID] = asset
		}
		if !assetSymbolPattern.MatchString(asset.Symbol) {
			add(path+".symbol", "REF_FORMAT", "symbol must be 2-16 uppercase letters or digits")
		} else if _, exists := assetSymbols[asset.Symbol]; exists {
			add(path+".symbol", "REF_DUPLICATE", "duplicate asset symbol %q", asset.Symbol)
		} else {
			assetSymbols[asset.Symbol] = struct{}{}
		}
		if strings.TrimSpace(asset.DisplayName) == "" || len(asset.DisplayName) > 64 {
			add(path+".display_name", "REF_RANGE", "display_name must be 1-64 characters")
		}
		if asset.Decimals < 0 || asset.Decimals > 18 {
			add(path+".decimals", "REF_RANGE", "decimals must be between 0 and 18")
		}
		if !validAssetKinds[asset.Kind] {
			add(path+".kind", "REF_ENUM", "kind must be crypto or stablecoin")
		}
		if !validAssetStatuses[asset.Status] {
			add(path+".status", "REF_ENUM", "status must be active, maintenance, or delisted")
		}
		validateOptionalAmount(add, path+".min_deposit", asset.MinDeposit)
		validateOptionalAmount(add, path+".max_deposit", asset.MaxDeposit)
		validateOptionalAmount(add, path+".min_withdrawal", asset.MinWithdrawal)
		validateOptionalAmount(add, path+".max_withdrawal", asset.MaxWithdrawal)
		validateAmount(add, path+".withdrawal_fee", asset.WithdrawalFee)
		validateMinMax(add, path+".min_deposit", asset.MinDeposit, path+".max_deposit", asset.MaxDeposit)
		validateMinMax(add, path+".min_withdrawal", asset.MinWithdrawal, path+".max_withdrawal", asset.MaxWithdrawal)
	}

	networks := make(map[string]Network, len(doc.Networks))
	for index, network := range doc.Networks {
		path := fmt.Sprintf("networks[%d]", index)
		if !networkIDPattern.MatchString(network.NetworkID) {
			add(path+".network_id", "REF_FORMAT", "network_id must be 2-32 lowercase letters, digits, '_' or '-'")
		} else if _, exists := networks[network.NetworkID]; exists {
			add(path+".network_id", "REF_DUPLICATE", "duplicate network_id %q", network.NetworkID)
		} else {
			networks[network.NetworkID] = network
		}
		if strings.TrimSpace(network.DisplayName) == "" || len(network.DisplayName) > 64 {
			add(path+".display_name", "REF_RANGE", "display_name must be 1-64 characters")
		}
		if !validNetworkFormats[network.AddressFormat] {
			add(path+".address_format", "REF_ENUM", "unsupported address_format %q", network.AddressFormat)
		}
		if network.AddressFormat == "evm_hex" {
			if network.ChainID == nil || !digitsPattern.MatchString(*network.ChainID) {
				add(path+".chain_id", "REF_REQUIRED", "chain_id is required and must be numeric for EVM networks")
			}
		}
		if network.NativeAssetID != nil {
			if _, ok := assets[*network.NativeAssetID]; !ok {
				add(path+".native_asset_id", "REF_UNKNOWN_REF", "unknown native asset %d", *network.NativeAssetID)
			}
		}
		if network.Confirmations < 0 || network.Confirmations > 1000 {
			add(path+".confirmations", "REF_RANGE", "confirmations must be between 0 and 1000")
		}
		if !validNetworkStatus[network.Status] {
			add(path+".status", "REF_ENUM", "status must be active, maintenance, or disabled")
		}
		validateAmount(add, path+".min_deposit", network.MinDeposit)
		validateAmount(add, path+".min_withdrawal", network.MinWithdrawal)
		validateAmount(add, path+".withdrawal_fee", network.WithdrawalFee)
	}

	mappings := make(map[string]AssetNetwork, len(doc.AssetNetworks))
	for index, mapping := range doc.AssetNetworks {
		path := fmt.Sprintf("asset_networks[%d]", index)
		key := fmt.Sprintf("%d/%s", mapping.AssetID, mapping.NetworkID)
		if _, exists := mappings[key]; exists {
			add(path, "REF_DUPLICATE", "duplicate asset/network mapping %s", key)
		}
		mappings[key] = mapping
		asset, assetOK := assets[mapping.AssetID]
		network, networkOK := networks[mapping.NetworkID]
		if !assetOK {
			add(path+".asset_id", "REF_UNKNOWN_REF", "unknown asset %d", mapping.AssetID)
		}
		if !networkOK {
			add(path+".network_id", "REF_UNKNOWN_REF", "unknown network %q", mapping.NetworkID)
		}
		if mapping.Decimals < 0 || mapping.Decimals > 18 {
			add(path+".decimals", "REF_RANGE", "decimals must be between 0 and 18")
		}
		if assetOK && mapping.Decimals > asset.Decimals {
			add(path+".decimals", "REF_RELATION", "network decimals must not exceed asset decimals")
		}
		validateAmount(add, path+".min_deposit", mapping.MinDeposit)
		validateAmount(add, path+".min_withdrawal", mapping.MinWithdrawal)
		validateAmount(add, path+".withdrawal_fee", mapping.WithdrawalFee)
		if mapping.Confirmations < 0 || mapping.Confirmations > 1000 {
			add(path+".confirmations", "REF_RANGE", "confirmations must be between 0 and 1000")
		}
		if !validNetworkStatus[mapping.Status] {
			add(path+".status", "REF_ENUM", "status must be active, maintenance, or disabled")
		}
		if networkOK {
			native := network.NativeAssetID != nil && *network.NativeAssetID == mapping.AssetID
			if native {
				if mapping.ContractAddress != nil && strings.TrimSpace(*mapping.ContractAddress) != "" {
					add(path+".contract_address", "REF_RELATION", "native asset mapping must not define a contract address")
				}
			} else {
				if mapping.ContractAddress == nil || strings.TrimSpace(*mapping.ContractAddress) == "" {
					add(path+".contract_address", "REF_REQUIRED", "token mapping requires a contract address")
				} else if err := validateContractAddress(network.AddressFormat, *mapping.ContractAddress); err != nil {
					add(path+".contract_address", "REF_ADDRESS", "%s", err.Error())
				}
			}
		}
	}

	// A network enabled for deposits or withdrawals must expose a mapping.
	for index, network := range doc.Networks {
		if !network.DepositEnabled && !network.WithdrawEnabled {
			continue
		}
		found := false
		for _, mapping := range doc.AssetNetworks {
			if mapping.NetworkID == network.NetworkID {
				found = true
				break
			}
		}
		if !found {
			add(fmt.Sprintf("networks[%d]", index), "REF_RELATION", "enabled network requires at least one asset mapping")
		}
	}

	schedules := make(map[string]FeeSchedule, len(doc.FeeSchedules))
	defaults := map[string]int{}
	for index, schedule := range doc.FeeSchedules {
		path := fmt.Sprintf("fee_schedules[%d]", index)
		if !configIDPattern.MatchString(schedule.ScheduleID) {
			add(path+".schedule_id", "REF_FORMAT", "schedule_id must be 2-64 lowercase letters, digits, '.', '_' or '-'")
		} else if _, exists := schedules[schedule.ScheduleID]; exists {
			add(path+".schedule_id", "REF_DUPLICATE", "duplicate schedule_id %q", schedule.ScheduleID)
		} else {
			schedules[schedule.ScheduleID] = schedule
		}
		if strings.TrimSpace(schedule.Name) == "" || len(schedule.Name) > 96 {
			add(path+".name", "REF_RANGE", "name must be 1-96 characters")
		}
		if !validMarketTypes[schedule.MarketType] {
			add(path+".market_type", "REF_ENUM", "market_type must be spot or perp")
		}
		if !validScheduleStatus[schedule.Status] {
			add(path+".status", "REF_ENUM", "status must be draft, active, or retired")
		}
		if _, err := time.Parse(time.RFC3339, schedule.EffectiveAt); err != nil {
			add(path+".effective_at", "REF_TIMESTAMP", "effective_at must be RFC3339 with timezone")
		}
		if schedule.IsDefault {
			defaults[schedule.MarketType]++
		}
		validateFeeTiers(add, path, schedule)
	}
	for marketType, count := range defaults {
		if count > 1 {
			add("fee_schedules", "REF_RELATION", "multiple default schedules for market_type %q", marketType)
		}
	}

	marginSets := make(map[string]MarginTierSet, len(doc.MarginTierSets))
	for index, marginSet := range doc.MarginTierSets {
		path := fmt.Sprintf("margin_tier_sets[%d]", index)
		if !configIDPattern.MatchString(marginSet.SetID) {
			add(path+".set_id", "REF_FORMAT", "set_id must be 2-64 lowercase letters, digits, '.', '_' or '-'")
		} else if _, exists := marginSets[marginSet.SetID]; exists {
			add(path+".set_id", "REF_DUPLICATE", "duplicate set_id %q", marginSet.SetID)
		} else {
			marginSets[marginSet.SetID] = marginSet
		}
		if strings.TrimSpace(marginSet.Name) == "" || len(marginSet.Name) > 96 {
			add(path+".name", "REF_RANGE", "name must be 1-96 characters")
		}
		if marginSet.MarketType != "perp" {
			add(path+".market_type", "REF_ENUM", "margin tier sets are only valid for perp instruments")
		}
		if !validScheduleStatus[marginSet.Status] {
			add(path+".status", "REF_ENUM", "status must be draft, active, or retired")
		}
		if _, err := time.Parse(time.RFC3339, marginSet.EffectiveAt); err != nil {
			add(path+".effective_at", "REF_TIMESTAMP", "effective_at must be RFC3339 with timezone")
		}
		validateMarginTiers(add, path+".tiers", marginSet.Tiers)
		if len(marginSet.Tiers) > 0 && marginSet.MaxLeverage != marginSet.Tiers[0].MaxLeverage {
			add(path+".max_leverage", "REF_RELATION", "max_leverage must equal the first tier leverage")
		}
	}

	instrumentSymbols := map[string]struct{}{}
	instrumentIDs := map[int32]struct{}{}
	for index, instrument := range doc.Instruments {
		path := fmt.Sprintf("instruments[%d]", index)
		if instrument.SymbolID <= 0 {
			add(path+".symbol_id", "REF_RANGE", "symbol_id must be positive")
		} else if _, exists := instrumentIDs[instrument.SymbolID]; exists {
			add(path+".symbol_id", "REF_DUPLICATE", "duplicate symbol_id %d", instrument.SymbolID)
		} else {
			instrumentIDs[instrument.SymbolID] = struct{}{}
		}
		if !instrumentSymbolPattern.MatchString(instrument.Symbol) {
			add(path+".symbol", "REF_FORMAT", "symbol must be 3-32 uppercase letters, digits, '_' or '-'")
		} else if _, exists := instrumentSymbols[instrument.Symbol]; exists {
			add(path+".symbol", "REF_DUPLICATE", "duplicate instrument symbol %q", instrument.Symbol)
		} else {
			instrumentSymbols[instrument.Symbol] = struct{}{}
		}
		if !validInstrumentKind[instrument.Kind] {
			add(path+".kind", "REF_ENUM", "kind must be SPOT or PERPETUAL")
		}
		base, baseOK := assets[instrument.Base]
		quote, quoteOK := assets[instrument.Quote]
		if !baseOK {
			add(path+".base", "REF_UNKNOWN_REF", "unknown base asset %d", instrument.Base)
		}
		if !quoteOK {
			add(path+".quote", "REF_UNKNOWN_REF", "unknown quote asset %d", instrument.Quote)
		}
		if instrument.Base == instrument.Quote {
			add(path, "REF_RELATION", "base and quote assets must differ")
		}
		if baseOK {
			validateSizeAlignment(add, path, ".lot_size", instrument.LotSize, instrument.QtyDecimals, int(base.Decimals), "lot_size", "qty_decimals")
		}
		if quoteOK {
			validateSizeAlignment(add, path, ".tick_size", instrument.TickSize, instrument.PriceDecimals, int(quote.Decimals), "tick_size", "price_decimals")
		}
		if instrument.MinQty <= 0 {
			add(path+".min_qty", "REF_RANGE", "min_qty must be positive")
		}
		if instrument.MaxQty < instrument.MinQty {
			add(path+".max_qty", "REF_RANGE", "max_qty must not be below min_qty")
		}
		if !instrument.MinNotional.IsPositive() {
			add(path+".min_notional", "REF_RANGE", "min_notional must be positive")
		}
		if instrument.MaxNotional.Cmp(instrument.MinNotional) < 0 {
			add(path+".max_notional", "REF_RANGE", "max_notional must not be below min_notional")
		}
		if instrument.MaxNotional.Exceeds(MaxNotionalAmount()) {
			add(path+".max_notional", "REF_OVERFLOW", "max_notional exceeds the supported atomic range")
		}
		if instrument.MaxQty > 0 && instrument.LotSize > 0 {
			if NewAmount(instrument.MaxQty).Mul(instrument.LotSize).Exceeds(maxAtomic) {
				add(path, "REF_OVERFLOW", "max_qty * lot_size exceeds the supported atomic range")
			}
		}
		if instrument.PriceBandBps < 0 || instrument.PriceBandBps > 10_000 {
			add(path+".price_band_bps", "REF_RANGE", "price_band_bps must be between 0 and 10000")
		}
		if !validInstrumentStat[instrument.Status] {
			add(path+".status", "REF_ENUM", "status must be ACTIVE, REDUCE_ONLY, HALTED, or DELISTED")
		}
		if len(instrument.AllowedOrderTypes) == 0 {
			add(path+".allowed_order_types", "REF_REQUIRED", "at least one order type is required")
		}
		for _, orderType := range instrument.AllowedOrderTypes {
			if !validOrderTypes[orderType] {
				add(path+".allowed_order_types", "REF_ENUM", "unsupported order type %q", orderType)
			}
			if instrument.Kind == "SPOT" && orderType == "CLOSE_POSITION" {
				add(path+".allowed_order_types", "REF_RELATION", "spot instruments cannot use CLOSE_POSITION")
			}
		}
		if len(instrument.AllowedTimeInForce) == 0 {
			add(path+".allowed_time_in_force", "REF_REQUIRED", "at least one time-in-force is required")
		}
		for _, tif := range instrument.AllowedTimeInForce {
			if !validTimeInForce[tif] {
				add(path+".allowed_time_in_force", "REF_ENUM", "unsupported time-in-force %q", tif)
			}
		}
		if _, err := time.Parse(time.RFC3339, instrument.ListedAt); err != nil {
			add(path+".listed_at", "REF_TIMESTAMP", "listed_at must be RFC3339 with timezone")
		}
		if instrument.DelistedAt != nil {
			listed, listedErr := time.Parse(time.RFC3339, instrument.ListedAt)
			delisted, delistedErr := time.Parse(time.RFC3339, *instrument.DelistedAt)
			if delistedErr != nil {
				add(path+".delisted_at", "REF_TIMESTAMP", "delisted_at must be RFC3339 with timezone")
			} else if listedErr == nil && !delisted.After(listed) {
				add(path+".delisted_at", "REF_TIMESTAMP", "delisted_at must be after listed_at")
			}
		}
		if instrument.Status == "DELISTED" && instrument.DelistedAt == nil {
			add(path+".delisted_at", "REF_REQUIRED", "DELISTED instruments require delisted_at")
		}
		schedule, scheduleOK := schedules[instrument.FeeScheduleID]
		if !scheduleOK {
			add(path+".fee_schedule_id", "REF_UNKNOWN_REF", "unknown fee schedule %q", instrument.FeeScheduleID)
		} else if schedule.MarketType != marketTypeForInstrument(instrument.Kind) {
			add(path+".fee_schedule_id", "REF_RELATION", "fee schedule market_type does not match instrument kind")
		} else if len(schedule.Tiers) > 0 {
			if instrument.MakerFeeBps != schedule.Tiers[0].MakerBps || instrument.TakerFeeBps != schedule.Tiers[0].TakerBps {
				add(path, "REF_RELATION", "maker_fee_bps and taker_fee_bps must match the referenced fee schedule base tier")
			}
		}

		if instrument.Kind == "SPOT" {
			if instrument.Contract != nil {
				add(path+".contract", "REF_RELATION", "spot instruments must not define a contract")
			}
			if instrument.MarginTierSetID != nil {
				add(path+".margin_tier_set_id", "REF_RELATION", "spot instruments must not reference margin tiers")
			}
			if len(instrument.MarginModeCapabilities) > 0 || len(instrument.PositionModeCapabilities) > 0 {
				add(path, "REF_RELATION", "spot instruments must not define futures margin capabilities")
			}
		} else if instrument.Kind == "PERPETUAL" {
			if instrument.Contract == nil {
				add(path+".contract", "REF_REQUIRED", "perpetual instruments require a contract specification")
			}
			if instrument.MarginTierSetID == nil {
				add(path+".margin_tier_set_id", "REF_REQUIRED", "perpetual instruments require a margin tier set")
			}
			if instrument.PriceSourcePolicyRef == nil || strings.TrimSpace(*instrument.PriceSourcePolicyRef) == "" {
				add(path+".price_source_policy_ref", "REF_REQUIRED", "perpetual instruments require a price-source policy reference")
			}
			validateModeCapabilities(add, path+".margin_mode_capabilities", instrument.MarginModeCapabilities, validMarginModes)
			validateModeCapabilities(add, path+".position_mode_capabilities", instrument.PositionModeCapabilities, validPositionModes)
			if instrument.Contract != nil {
				validateContract(add, path+".contract", instrument)
				if instrument.MarginTierSetID != nil {
					marginSet, setOK := marginSets[*instrument.MarginTierSetID]
					if !setOK {
						add(path+".margin_tier_set_id", "REF_UNKNOWN_REF", "unknown margin tier set %q", *instrument.MarginTierSetID)
					} else {
						if instrument.Contract.MaxLeverage != marginSet.MaxLeverage {
							add(path+".contract.max_leverage", "REF_RELATION", "contract max_leverage must equal the margin tier set max_leverage")
						}
						if !marginTiersEqual(instrument.Contract.MarginTiers, marginSet.Tiers) {
							add(path+".contract.margin_tiers", "REF_RELATION", "contract margin tiers must exactly match the referenced margin tier set")
						}
						if len(marginSet.Tiers) > 0 && instrument.MaxNotional.Cmp(marginSet.Tiers[len(marginSet.Tiers)-1].NotionalCap) > 0 {
							add(path+".max_notional", "REF_RELATION", "instrument max_notional exceeds the last margin tier cap")
						}
					}
				}
			}
		}
	}

	// Every market type that has instruments must have a default fee schedule.
	for _, instrument := range doc.Instruments {
		marketType := marketTypeForInstrument(instrument.Kind)
		if schedulesForMarketTypeDefault(schedules, marketType) == 0 {
			add("fee_schedules", "REF_REQUIRED", "at least one default %s fee schedule is required", marketType)
		}
	}

	presentationIDs := map[string]struct{}{}
	for index, policy := range doc.PresentationPolicies {
		path := fmt.Sprintf("presentation_policies[%d]", index)
		if !configIDPattern.MatchString(policy.PolicyID) {
			add(path+".policy_id", "REF_FORMAT", "policy_id must be 2-64 lowercase letters, digits, '.', '_' or '-'")
		} else if _, exists := presentationIDs[policy.PolicyID]; exists {
			add(path+".policy_id", "REF_DUPLICATE", "duplicate policy_id %q", policy.PolicyID)
		} else {
			presentationIDs[policy.PolicyID] = struct{}{}
		}
		if _, ok := instrumentSymbols[policy.Symbol]; !ok {
			add(path+".symbol", "REF_UNKNOWN_REF", "unknown instrument symbol %q", policy.Symbol)
		}
		validateVenueList(add, path+".source_priority", policy.SourcePriority, true)
		validateVenueList(add, path+".fallback_priority", policy.FallbackPriority, false)
		sourceSet := set(policy.SourcePriority...)
		for _, fallback := range policy.FallbackPriority {
			if _, overlap := sourceSet[fallback]; overlap {
				add(path+".fallback_priority", "REF_RELATION", "fallback source %q duplicates a primary source", fallback)
			}
		}
		if policy.FreshnessMS < 100 || policy.FreshnessMS > 60_000 {
			add(path+".freshness_ms", "REF_RANGE", "freshness_ms must be between 100 and 60000")
		}
		if policy.StalenessMS <= policy.FreshnessMS || policy.StalenessMS > 300_000 {
			add(path+".staleness_ms", "REF_RANGE", "staleness_ms must exceed freshness_ms and be at most 300000")
		}
		if policy.MaxDeviationBps < 0 || policy.MaxDeviationBps > 1_000 {
			add(path+".max_deviation_bps", "REF_RANGE", "max_deviation_bps must be between 0 and 1000")
		}
		if !validQuoteMethods[policy.QuoteMethod] {
			add(path+".quote_method", "REF_ENUM", "unsupported quote_method %q", policy.QuoteMethod)
		}
		if policy.MaxQuoteOffsetBps < 0 || policy.MaxQuoteOffsetBps > policy.MaxDeviationBps {
			add(path+".max_quote_offset_bps", "REF_RANGE", "max_quote_offset_bps must be between 0 and max_deviation_bps")
		}
		if policy.AuthoritativeForRisk {
			add(path+".authoritative_for_risk", "REF_RELATION", "presentation policy must never be authoritative for risk")
		}
		if policy.ShowSyntheticVolume {
			add(path+".show_synthetic_volume", "REF_RELATION", "synthetic volume is not permitted; it must never be presented as executed volume")
		}
		if strings.TrimSpace(policy.SyntheticVolumeLabel) != "" {
			add(path+".synthetic_volume_label", "REF_RELATION", "synthetic_volume_label must be empty because synthetic volume is disabled")
		}
		if !validPolicyStatus[policy.Status] {
			add(path+".status", "REF_ENUM", "status must be active or disabled")
		}
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Path < issues[j].Path
	})
	return issues
}

func validateContract(add func(string, string, string, ...any), path string, instrument Instrument) {
	contract := instrument.Contract
	if contract == nil {
		return
	}
	if contract.ContractSize.Numerator <= 0 || contract.ContractSize.Denominator <= 0 {
		add(path+".contract_size", "REF_RANGE", "contract size numerator and denominator must be positive")
	}
	if contract.SettlementAsset != instrument.Quote {
		add(path+".settlement_asset", "REF_RELATION", "linear contract settlement asset must equal the quote asset")
	}
	if contract.MaxLeverage <= 0 || contract.MaxLeverage > 125 {
		add(path+".max_leverage", "REF_RANGE", "max_leverage must be between 1 and 125")
	}
	if contract.FundingIntervalSecs <= 0 || contract.FundingIntervalSecs > 604_800 {
		add(path+".funding_interval_secs", "REF_RANGE", "funding_interval_secs must be between 1 and 604800")
	}
	if contract.FundingCapBps < 0 || contract.FundingCapBps > 10_000 {
		add(path+".funding_cap_bps", "REF_RANGE", "funding_cap_bps must be between 0 and 10000")
	}
	if contract.LiquidationFeeBps < 0 || contract.LiquidationFeeBps > 10_000 {
		add(path+".liquidation_fee_bps", "REF_RANGE", "liquidation_fee_bps must be between 0 and 10000")
	}
	validateMarginTiers(add, path+".margin_tiers", contract.MarginTiers)
}

func validateMarginTiers(add func(string, string, string, ...any), path string, tiers []MarginTier) {
	maxAtomic := MaxAtomicAmount()
	maxNotional := MaxNotionalAmount()
	if len(tiers) == 0 {
		add(path, "REF_REQUIRED", "at least one margin tier is required")
		return
	}
	var previous *MarginTier
	for index, tier := range tiers {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if tier.NotionalFloor.IsNegative() || tier.NotionalCap.IsNegative() {
			add(itemPath, "REF_RANGE", "notional bounds must be non-negative")
		}
		if tier.NotionalCap.Cmp(tier.NotionalFloor) <= 0 {
			add(itemPath, "REF_RANGE", "notional_cap must exceed notional_floor")
		}
		if tier.MaxLeverage <= 0 || tier.MaxLeverage > 125 {
			add(itemPath+".max_leverage", "REF_RANGE", "max_leverage must be between 1 and 125")
		}
		if tier.MMRBps < 0 || tier.MMRBps > 10_000 {
			add(itemPath+".mmr_bps", "REF_RANGE", "mmr_bps must be between 0 and 10000")
		}
		if tier.MaintenanceAmount.IsNegative() || tier.MaintenanceAmount.Exceeds(maxAtomic) {
			add(itemPath+".maintenance_amount", "REF_RANGE", "maintenance_amount is outside the supported atomic range")
		}
		if tier.NotionalCap.Exceeds(maxNotional) {
			add(itemPath+".notional_cap", "REF_OVERFLOW", "notional_cap exceeds the supported atomic range")
		}
		if previous == nil {
			if !tier.NotionalFloor.IsZero() {
				add(itemPath+".notional_floor", "REF_TIER_GAP", "first margin tier must start at zero")
			}
			if !tier.MaintenanceAmount.IsZero() {
				add(itemPath+".maintenance_amount", "REF_TIER_RELATION", "first maintenance amount must be zero")
			}
		} else {
			if tier.NotionalFloor.Cmp(previous.NotionalCap) < 0 {
				add(itemPath+".notional_floor", "REF_TIER_OVERLAP", "margin tier overlaps the previous tier")
			} else if tier.NotionalFloor.Cmp(previous.NotionalCap) > 0 {
				add(itemPath+".notional_floor", "REF_TIER_GAP", "margin tiers must be contiguous")
			}
			if tier.MaxLeverage > previous.MaxLeverage {
				add(itemPath+".max_leverage", "REF_TIER_ORDER", "maximum leverage must not increase across tiers")
			}
			if tier.MMRBps < previous.MMRBps {
				add(itemPath+".mmr_bps", "REF_TIER_ORDER", "maintenance margin rate must not decrease across tiers")
			}
			delta := int64(tier.MMRBps) - int64(previous.MMRBps)
			product := tier.NotionalFloor.Mul(delta)
			quotient, remainder, err := product.QuoRem(10_000)
			if err != nil || remainder != 0 {
				add(itemPath+".maintenance_amount", "REF_TIER_RELATION", "maintenance amount must be integral")
			} else {
				expected := previous.MaintenanceAmount.Add(quotient)
				if !tier.MaintenanceAmount.Equal(expected) {
					add(itemPath+".maintenance_amount", "REF_TIER_RELATION", "maintenance amount must be derived from the previous tier")
				}
			}
		}
		previous = &tiers[index]
	}
}

func validateFeeTiers(add func(string, string, string, ...any), path string, schedule FeeSchedule) {
	maxNotional := MaxNotionalAmount()
	if len(schedule.Tiers) == 0 {
		add(path+".tiers", "REF_REQUIRED", "at least one fee tier is required")
		return
	}
	var previous *FeeTier
	for index, tier := range schedule.Tiers {
		itemPath := fmt.Sprintf("%s.tiers[%d]", path, index)
		if tier.TierIndex != int32(index) {
			add(itemPath+".tier_index", "REF_TIER_ORDER", "tier_index must be contiguous starting at zero")
		}
		if tier.MinVolume.IsNegative() || tier.MinVolume.Exceeds(maxNotional) {
			add(itemPath+".min_volume", "REF_RANGE", "min_volume is outside the supported range")
		}
		if tier.MaxVolume != nil {
			if tier.MaxVolume.Cmp(tier.MinVolume) <= 0 || tier.MaxVolume.Exceeds(maxNotional) {
				add(itemPath+".max_volume", "REF_RANGE", "max_volume must exceed min_volume and remain within the supported range")
			}
			if index == len(schedule.Tiers)-1 {
				add(itemPath+".max_volume", "REF_TIER_ORDER", "the final fee tier must be open-ended")
			}
		} else if index != len(schedule.Tiers)-1 {
			add(itemPath+".max_volume", "REF_TIER_ORDER", "only the final fee tier may be open-ended")
		}
		if tier.MakerBps < -100 || tier.MakerBps > 10_000 {
			add(itemPath+".maker_bps", "REF_RANGE", "maker_bps must be between -100 and 10000")
		}
		if tier.MakerBps < 0 && !schedule.RebateEnabled {
			add(itemPath+".maker_bps", "REF_RELATION", "negative maker fees require rebate_enabled")
		}
		if tier.TakerBps < 0 || tier.TakerBps > 10_000 {
			add(itemPath+".taker_bps", "REF_RANGE", "taker_bps must be between 0 and 10000")
		}
		if previous == nil {
			if !tier.MinVolume.IsZero() {
				add(itemPath+".min_volume", "REF_TIER_GAP", "first fee tier must start at zero volume")
			}
		} else {
			if previous.MaxVolume == nil {
				add(itemPath, "REF_TIER_ORDER", "no tier may follow an open-ended tier")
			} else if tier.MinVolume.Cmp(*previous.MaxVolume) < 0 {
				add(itemPath+".min_volume", "REF_TIER_OVERLAP", "fee tier overlaps the previous tier")
			} else if tier.MinVolume.Cmp(*previous.MaxVolume) > 0 {
				add(itemPath+".min_volume", "REF_TIER_GAP", "fee tiers must be contiguous")
			}
			if tier.TakerBps > previous.TakerBps {
				add(itemPath+".taker_bps", "REF_TIER_ORDER", "taker fees must not increase with volume")
			}
			if tier.MakerBps > previous.MakerBps {
				add(itemPath+".maker_bps", "REF_TIER_ORDER", "maker fees must not increase with volume")
			}
		}
		previous = &schedule.Tiers[index]
	}
}

func validateSizeAlignment(add func(string, string, string, ...any), instrumentPath, suffix string, size int64, sizeDecimals int16, assetDecimals int, sizeName, decimalsName string) {
	if size <= 0 {
		add(instrumentPath+suffix, "REF_RANGE", "%s must be positive", sizeName)
		return
	}
	if sizeDecimals < 0 || int(sizeDecimals) > assetDecimals {
		add(instrumentPath+"."+decimalsName, "REF_RANGE", "%s must be between 0 and the asset decimals", decimalsName)
		return
	}
	unit := pow10(assetDecimals - int(sizeDecimals))
	if unit <= 0 {
		add(instrumentPath+suffix, "REF_OVERFLOW", "size unit is outside the supported range")
		return
	}
	if size < unit || size%unit != 0 {
		add(instrumentPath+suffix, "REF_ALIGNMENT", "%s must be a positive multiple of %d atomic units", sizeName, unit)
	}
}

func validateAmount(add func(string, string, string, ...any), path string, value Amount) {
	if value.IsNegative() || value.Exceeds(MaxAtomicAmount()) {
		add(path, "REF_RANGE", "amount is outside the supported atomic range")
	}
}

func validateOptionalAmount(add func(string, string, string, ...any), path string, value *Amount) {
	if value == nil {
		return
	}
	validateAmount(add, path, *value)
}

func validateMinMax(add func(string, string, string, ...any), minPath string, min *Amount, maxPath string, max *Amount) {
	if min == nil || max == nil {
		return
	}
	if max.Cmp(*min) < 0 {
		add(maxPath, "REF_RANGE", "maximum must not be below minimum")
	}
}

func validateModeCapabilities(add func(string, string, string, ...any), path string, values []string, allowed map[string]bool) {
	if len(values) == 0 {
		add(path, "REF_REQUIRED", "at least one capability is required")
		return
	}
	for _, value := range values {
		if !allowed[value] {
			add(path, "REF_ENUM", "unsupported capability %q", value)
		}
	}
}

func validateVenueList(add func(string, string, string, ...any), path string, values []string, required bool) {
	if len(values) == 0 {
		if required {
			add(path, "REF_REQUIRED", "at least one market-data source is required")
		}
		return
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !venuePattern.MatchString(value) {
			add(path, "REF_FORMAT", "invalid market-data source %q", value)
		}
		if _, exists := seen[value]; exists {
			add(path, "REF_DUPLICATE", "duplicate market-data source %q", value)
		}
		seen[value] = struct{}{}
	}
}

func validateContractAddress(format, address string) error {
	switch format {
	case "evm_hex":
		if !evmAddressPattern.MatchString(address) {
			return fmt.Errorf("EVM contract address must be 0x followed by 40 hex characters")
		}
	case "tron_base58":
		if !tronAddressPattern.MatchString(address) {
			return fmt.Errorf("TRON contract address has an invalid base58 format")
		}
	case "solana_base58":
		if !solanaAddressPattern.MatchString(address) {
			return fmt.Errorf("Solana contract address has an invalid base58 format")
		}
	case "bitcoin":
		return fmt.Errorf("the Bitcoin network does not support token contract addresses")
	default:
		return fmt.Errorf("no contract address format is defined for %q", format)
	}
	return nil
}

func marketTypeForInstrument(kind string) string {
	if kind == "PERPETUAL" {
		return "perp"
	}
	return "spot"
}

func schedulesForMarketTypeDefault(schedules map[string]FeeSchedule, marketType string) int {
	count := 0
	for _, schedule := range schedules {
		if schedule.MarketType == marketType && schedule.IsDefault && schedule.Status == "active" {
			count++
		}
	}
	return count
}

func marginTiersEqual(a, b []MarginTier) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if !a[index].NotionalFloor.Equal(b[index].NotionalFloor) ||
			!a[index].NotionalCap.Equal(b[index].NotionalCap) ||
			a[index].MaxLeverage != b[index].MaxLeverage ||
			a[index].MMRBps != b[index].MMRBps ||
			!a[index].MaintenanceAmount.Equal(b[index].MaintenanceAmount) {
			return false
		}
	}
	return true
}

func pow10(exponent int) int64 {
	if exponent < 0 || exponent > 18 {
		return 0
	}
	value := int64(1)
	for i := 0; i < exponent; i++ {
		value *= 10
	}
	return value
}

func set(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
