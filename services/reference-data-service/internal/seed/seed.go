// Package seed supplies non-production reference-data fixtures.
//
// These values are synthetic test fixtures. They are not exchange-published
// tick sizes, fee schedules, margin tiers, or live custody metadata. Contract
// addresses are well-known burn or placeholder addresses and are never
// withdrawal destinations. No private key, seed phrase, or live wallet address
// appears in this package.
package seed

import (
	"fmt"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
)

func amount(value int64) refdata.Amount { return refdata.NewAmount(value) }

func amountString(value string) refdata.Amount { return refdata.MustAmount(value) }

func amountPtr(value refdata.Amount) *refdata.Amount { return &value }

func int32Ptr(value int32) *int32 { return &value }

func stringPtr(value string) *string { return &value }

// Document returns the complete B04 fixture configuration.
func Document() refdata.Document {
	btc := refdata.Asset{
		AssetID: 1, Symbol: "BTC", DisplayName: "Bitcoin", Decimals: 8, Kind: "crypto",
		Status: "active", TradingEnabled: true, TransferEnabled: true,
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amountPtr(amount(100_000)), MaxDeposit: amountPtr(amountString("10000000000")),
		MinWithdrawal: amountPtr(amount(200_000)), MaxWithdrawal: amountPtr(amountString("10000000000")),
		WithdrawalFee: amount(50_000),
	}
	eth := refdata.Asset{
		AssetID: 2, Symbol: "ETH", DisplayName: "Ether", Decimals: 18, Kind: "crypto",
		Status: "active", TradingEnabled: true, TransferEnabled: true,
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit:    amountPtr(amountString("1000000000000000")),
		MaxDeposit:    amountPtr(amountString("1000000000000000000000")),
		MinWithdrawal: amountPtr(amountString("2000000000000000")),
		MaxWithdrawal: amountPtr(amountString("1000000000000000000000")),
		WithdrawalFee: amountString("1000000000000000"),
	}
	usdt := refdata.Asset{
		AssetID: 3, Symbol: "USDT", DisplayName: "Tether USD", Decimals: 6, Kind: "stablecoin",
		Status: "active", TradingEnabled: true, TransferEnabled: true,
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amountPtr(amountString("1000000")), MaxDeposit: amountPtr(amountString("10000000000000")),
		MinWithdrawal: amountPtr(amountString("2000000")), MaxWithdrawal: amountPtr(amountString("10000000000000")),
		WithdrawalFee: amountString("1000000"),
	}

	bitcoinNetwork := refdata.Network{
		NetworkID: "bitcoin", DisplayName: "Bitcoin", NativeAssetID: int32Ptr(1),
		AddressFormat: "bitcoin", Confirmations: 3, Status: "active",
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amount(100_000), MinWithdrawal: amount(200_000), WithdrawalFee: amount(50_000),
	}
	ethereumNetwork := refdata.Network{
		NetworkID: "ethereum", DisplayName: "Ethereum", ChainID: stringPtr("1"), NativeAssetID: int32Ptr(2),
		AddressFormat: "evm_hex", Confirmations: 12, Status: "active",
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amountString("1000000000000000"), MinWithdrawal: amountString("2000000000000000"),
		WithdrawalFee: amountString("1000000000000000"),
	}
	tronNetwork := refdata.Network{
		NetworkID: "tron", DisplayName: "TRON", AddressFormat: "tron_base58",
		Confirmations: 20, Status: "active",
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amountString("1000000"), MinWithdrawal: amountString("2000000"), WithdrawalFee: amountString("1000000"),
	}

	mappings := []refdata.AssetNetwork{
		{
			AssetID: 1, NetworkID: "bitcoin", Decimals: 8,
			DepositEnabled: true, WithdrawEnabled: true,
			MinDeposit: amount(100_000), MinWithdrawal: amount(200_000), WithdrawalFee: amount(50_000),
			Confirmations: 3, Status: "active",
		},
		{
			AssetID: 2, NetworkID: "ethereum", Decimals: 18,
			DepositEnabled: true, WithdrawEnabled: true,
			MinDeposit: amountString("1000000000000000"), MinWithdrawal: amountString("2000000000000000"),
			WithdrawalFee: amountString("1000000000000000"), Confirmations: 12, Status: "active",
		},
		{
			// Fixture-only token contract. This is a burn address, not a custody
			// address, and must never be used as a withdrawal destination.
			AssetID: 3, NetworkID: "ethereum", ContractAddress: stringPtr("0x000000000000000000000000000000000000dEaD"),
			Decimals: 6, DepositEnabled: true, WithdrawEnabled: true,
			MinDeposit: amountString("1000000"), MinWithdrawal: amountString("2000000"),
			WithdrawalFee: amountString("1000000"), Confirmations: 12, Status: "active",
		},
		{
			// Fixture-only TRON token contract (well-known burn address).
			AssetID: 3, NetworkID: "tron", ContractAddress: stringPtr("T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"),
			Decimals: 6, DepositEnabled: true, WithdrawEnabled: true,
			MinDeposit: amountString("1000000"), MinWithdrawal: amountString("2000000"),
			WithdrawalFee: amountString("1000000"), Confirmations: 20, Status: "active",
		},
	}

	marginTiers := []refdata.MarginTier{
		{NotionalFloor: amount(0), NotionalCap: amountString("50000000000"), MaxLeverage: 125, MMRBps: 40, MaintenanceAmount: amount(0)},
		{NotionalFloor: amountString("50000000000"), NotionalCap: amountString("250000000000"), MaxLeverage: 100, MMRBps: 50, MaintenanceAmount: amountString("50000000")},
		{NotionalFloor: amountString("250000000000"), NotionalCap: amountString("1000000000000"), MaxLeverage: 50, MMRBps: 100, MaintenanceAmount: amountString("1300000000")},
	}

	spotDefault := refdata.FeeSchedule{
		ScheduleID: "spot-default", Name: "Spot default (fixture)", MarketType: "spot", IsDefault: true,
		AccountCategory: "retail", EffectiveAt: "2026-01-01T00:00:00Z", Status: "active",
		Tiers: []refdata.FeeTier{
			{TierIndex: 0, MinVolume: amount(0), MaxVolume: amountPtr(amountString("100000000000")), MakerBps: 10, TakerBps: 10},
			{TierIndex: 1, MinVolume: amountString("100000000000"), MaxVolume: amountPtr(amountString("1000000000000")), MakerBps: 8, TakerBps: 8},
			{TierIndex: 2, MinVolume: amountString("1000000000000"), MaxVolume: nil, MakerBps: 5, TakerBps: 5},
		},
	}
	spotMarketMaker := refdata.FeeSchedule{
		ScheduleID: "spot-market-maker", Name: "Spot market maker (fixture)", MarketType: "spot",
		AccountCategory: "market_maker", EffectiveAt: "2026-01-01T00:00:00Z", Status: "active",
		RebateEnabled: true,
		Tiers: []refdata.FeeTier{
			{TierIndex: 0, MinVolume: amount(0), MaxVolume: amountPtr(amountString("1000000000000")), MakerBps: -1, TakerBps: 3},
			{TierIndex: 1, MinVolume: amountString("1000000000000"), MaxVolume: nil, MakerBps: -2, TakerBps: 3},
		},
	}
	perpDefault := refdata.FeeSchedule{
		ScheduleID: "perp-default", Name: "Perpetual default (fixture)", MarketType: "perp", IsDefault: true,
		AccountCategory: "retail", EffectiveAt: "2026-01-01T00:00:00Z", Status: "active",
		Tiers: []refdata.FeeTier{
			{TierIndex: 0, MinVolume: amount(0), MaxVolume: amountPtr(amountString("100000000000")), MakerBps: 2, TakerBps: 5},
			{TierIndex: 1, MinVolume: amountString("100000000000"), MaxVolume: amountPtr(amountString("1000000000000")), MakerBps: 1, TakerBps: 4},
			{TierIndex: 2, MinVolume: amountString("1000000000000"), MaxVolume: nil, MakerBps: 0, TakerBps: 3},
		},
	}

	btcPerpTiers := refdata.MarginTierSet{
		SetID: "btc-perp-tiers", Name: "BTC perp margin tiers (fixture)", MarketType: "perp",
		EffectiveAt: "2026-01-01T00:00:00Z", Status: "active", MaxLeverage: 125, Tiers: marginTiers,
	}
	ethPerpTiers := refdata.MarginTierSet{
		SetID: "eth-perp-tiers", Name: "ETH perp margin tiers (fixture)", MarketType: "perp",
		EffectiveAt: "2026-01-01T00:00:00Z", Status: "active", MaxLeverage: 125, Tiers: marginTiers,
	}

	btcPerpContract := &refdata.ContractSpec{
		ContractSize: refdata.Multiplier{Numerator: 1, Denominator: 1}, SettlementAsset: 3,
		MaxLeverage: 125, MarginTiers: marginTiers, FundingIntervalSecs: 28_800,
		FundingCapBps: 75, LiquidationFeeBps: 50, ADLEnabled: true,
	}
	ethPerpContract := &refdata.ContractSpec{
		ContractSize: refdata.Multiplier{Numerator: 1, Denominator: 1}, SettlementAsset: 3,
		MaxLeverage: 125, MarginTiers: marginTiers, FundingIntervalSecs: 28_800,
		FundingCapBps: 75, LiquidationFeeBps: 50, ADLEnabled: true,
	}

	instruments := []refdata.Instrument{
		{
			SymbolID: 1, Symbol: "BTCUSDT", Kind: "SPOT", Base: 1, Quote: 3,
			TickSize: 100_000, LotSize: 100_000, PriceDecimals: 1, QtyDecimals: 5,
			MinQty: 1, MaxQty: 1_000_000, MinNotional: amountString("5000000"), MaxNotional: amountString("1000000000000"),
			PriceBandBps: 500, MakerFeeBps: 10, TakerFeeBps: 10, Status: "ACTIVE",
			AllowedOrderTypes:  []string{"LIMIT", "MARKET", "STOP_LIMIT", "STOP_MARKET"},
			AllowedTimeInForce: []string{"GTC", "IOC", "FOK", "DAY"},
			ListedAt:           "2026-01-01T00:00:00Z", FeeScheduleID: "spot-default",
		},
		{
			SymbolID: 2, Symbol: "ETHUSDT", Kind: "SPOT", Base: 2, Quote: 3,
			TickSize: 10_000, LotSize: 1_000_000_000_000_000,
			PriceDecimals: 2, QtyDecimals: 3,
			MinQty: 1, MaxQty: 1_000_000, MinNotional: amountString("5000000"), MaxNotional: amountString("1000000000000"),
			PriceBandBps: 500, MakerFeeBps: -1, TakerFeeBps: 3, Status: "ACTIVE",
			AllowedOrderTypes:  []string{"LIMIT", "MARKET", "STOP_LIMIT", "STOP_MARKET"},
			AllowedTimeInForce: []string{"GTC", "IOC", "FOK", "DAY"},
			ListedAt:           "2026-01-01T00:00:00Z", FeeScheduleID: "spot-market-maker",
		},
		{
			SymbolID: 3, Symbol: "BTCUSDT-PERP", Kind: "PERPETUAL", Base: 1, Quote: 3,
			TickSize: 100_000, LotSize: 100_000, PriceDecimals: 1, QtyDecimals: 5,
			MinQty: 1, MaxQty: 1_000_000, MinNotional: amountString("5000000"), MaxNotional: amountString("500000000000"),
			PriceBandBps: 500, MakerFeeBps: 2, TakerFeeBps: 5, Status: "ACTIVE",
			Contract:           btcPerpContract,
			AllowedOrderTypes:  []string{"LIMIT", "MARKET", "STOP_LIMIT", "STOP_MARKET", "TAKE_PROFIT", "CLOSE_POSITION"},
			AllowedTimeInForce: []string{"GTC", "IOC", "FOK"},
			ListedAt:           "2026-01-01T00:00:00Z", FeeScheduleID: "perp-default",
			MarginTierSetID: stringPtr("btc-perp-tiers"), RiskLimitRef: stringPtr("risk-limits-btc-perp-v1"),
			PriceSourcePolicyRef:   stringPtr("canonical-btc-perp-v1"),
			MarginModeCapabilities: []string{"cross", "isolated"}, PositionModeCapabilities: []string{"one_way", "hedge"},
		},
		{
			SymbolID: 4, Symbol: "ETHUSDT-PERP", Kind: "PERPETUAL", Base: 2, Quote: 3,
			TickSize: 10_000, LotSize: 1_000_000_000_000_000, PriceDecimals: 2, QtyDecimals: 3,
			MinQty: 1, MaxQty: 1_000_000, MinNotional: amountString("5000000"), MaxNotional: amountString("500000000000"),
			PriceBandBps: 500, MakerFeeBps: 2, TakerFeeBps: 5, Status: "ACTIVE",
			Contract:           ethPerpContract,
			AllowedOrderTypes:  []string{"LIMIT", "MARKET", "STOP_LIMIT", "STOP_MARKET", "TAKE_PROFIT", "CLOSE_POSITION"},
			AllowedTimeInForce: []string{"GTC", "IOC", "FOK"},
			ListedAt:           "2026-01-01T00:00:00Z", FeeScheduleID: "perp-default",
			MarginTierSetID: stringPtr("eth-perp-tiers"), RiskLimitRef: stringPtr("risk-limits-eth-perp-v1"),
			PriceSourcePolicyRef:   stringPtr("canonical-eth-perp-v1"),
			MarginModeCapabilities: []string{"cross", "isolated"}, PositionModeCapabilities: []string{"one_way", "hedge"},
		},
	}

	presentation := []refdata.PresentationPolicy{
		{
			PolicyID: "btcusdt-presentation", Symbol: "BTCUSDT",
			SourcePriority: []string{"bybit", "mexc"}, FallbackPriority: []string{"nexora_index"},
			FreshnessMS: 1_000, StalenessMS: 3_000, MaxDeviationBps: 50,
			QuoteMethod: "midpoint", MaxQuoteOffsetBps: 10, AuthoritativeForRisk: false, Status: "active",
		},
		{
			PolicyID: "ethusdt-presentation", Symbol: "ETHUSDT",
			SourcePriority: []string{"bybit", "mexc"}, FallbackPriority: []string{"nexora_index"},
			FreshnessMS: 1_000, StalenessMS: 3_000, MaxDeviationBps: 50,
			QuoteMethod: "midpoint", MaxQuoteOffsetBps: 10, AuthoritativeForRisk: false, Status: "active",
		},
		{
			PolicyID: "btcusdt-perp-presentation", Symbol: "BTCUSDT-PERP",
			SourcePriority: []string{"bybit", "mexc"}, FallbackPriority: []string{"nexora_index"},
			FreshnessMS: 1_000, StalenessMS: 3_000, MaxDeviationBps: 75,
			QuoteMethod: "midpoint", MaxQuoteOffsetBps: 10, AuthoritativeForRisk: false, Status: "active",
		},
		{
			PolicyID: "ethusdt-perp-presentation", Symbol: "ETHUSDT-PERP",
			SourcePriority: []string{"bybit", "mexc"}, FallbackPriority: []string{"nexora_index"},
			FreshnessMS: 1_000, StalenessMS: 3_000, MaxDeviationBps: 75,
			QuoteMethod: "midpoint", MaxQuoteOffsetBps: 10, AuthoritativeForRisk: false, Status: "active",
		},
	}

	return refdata.Document{
		SchemaVersion:        refdata.SchemaVersion,
		Assets:               []refdata.Asset{btc, eth, usdt},
		Networks:             []refdata.Network{bitcoinNetwork, ethereumNetwork, tronNetwork},
		AssetNetworks:        mappings,
		Instruments:          instruments,
		FeeSchedules:         []refdata.FeeSchedule{spotDefault, spotMarketMaker, perpDefault},
		MarginTierSets:       []refdata.MarginTierSet{btcPerpTiers, ethPerpTiers},
		PresentationPolicies: presentation,
	}
}

// Validate checks the fixture before it is loaded.
func Validate() error {
	issues := Document().Validate()
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("seed fixture is invalid: %s at %s", issues[0].Message, issues[0].Path)
}
