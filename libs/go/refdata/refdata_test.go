package refdata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func amt(value int64) Amount { return NewAmount(value) }

func amtPtr(value int64) *Amount {
	amount := NewAmount(value)
	return &amount
}

func int32Ptr(value int32) *int32 { return &value }

func stringPtr(value string) *string { return &value }

// validDocument returns a small internally consistent configuration used by
// package tests. It is intentionally independent of service fixtures.
func validDocument() Document {
	assetBTC := Asset{
		AssetID: 1, Symbol: "BTC", DisplayName: "Bitcoin", Decimals: 8, Kind: "crypto",
		Status: "active", TradingEnabled: true, TransferEnabled: true,
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amtPtr(100_000), MinWithdrawal: amtPtr(200_000), WithdrawalFee: amt(50_000),
	}
	assetUSDT := Asset{
		AssetID: 2, Symbol: "USDT", DisplayName: "Tether USD", Decimals: 6, Kind: "stablecoin",
		Status: "active", TradingEnabled: true, TransferEnabled: true,
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amtPtr(1_000_000), MinWithdrawal: amtPtr(2_000_000), WithdrawalFee: amt(1_000_000),
	}
	network := Network{
		NetworkID: "bitcoin", DisplayName: "Bitcoin", NativeAssetID: int32Ptr(1),
		AddressFormat: "bitcoin", Confirmations: 3, Status: "active",
		DepositEnabled: true, WithdrawEnabled: true,
		MinDeposit: amt(100_000), MinWithdrawal: amt(200_000), WithdrawalFee: amt(50_000),
	}
	mapping := AssetNetwork{
		AssetID: 1, NetworkID: "bitcoin", Decimals: 8, DepositEnabled: true,
		WithdrawEnabled: true, MinDeposit: amt(100_000), MinWithdrawal: amt(200_000),
		WithdrawalFee: amt(50_000), Confirmations: 3, Status: "active",
	}
	fee := FeeSchedule{
		ScheduleID: "spot-default", Name: "Spot default", MarketType: "spot", IsDefault: true,
		AccountCategory: "retail", EffectiveAt: "2026-01-01T00:00:00Z", Status: "active",
		Tiers: []FeeTier{{TierIndex: 0, MinVolume: amt(0), MaxVolume: nil, MakerBps: 10, TakerBps: 10}},
	}
	tiers := []MarginTier{
		{NotionalFloor: amt(0), NotionalCap: amt(50_000_000_000), MaxLeverage: 125, MMRBps: 40, MaintenanceAmount: amt(0)},
		{NotionalFloor: amt(50_000_000_000), NotionalCap: amt(250_000_000_000), MaxLeverage: 100, MMRBps: 50, MaintenanceAmount: amt(50_000_000)},
		{NotionalFloor: amt(250_000_000_000), NotionalCap: amt(1_000_000_000_000), MaxLeverage: 50, MMRBps: 100, MaintenanceAmount: amt(1_300_000_000)},
	}
	perpFee := FeeSchedule{
		ScheduleID: "perp-default", Name: "Perp default", MarketType: "perp", IsDefault: true,
		AccountCategory: "retail", EffectiveAt: "2026-01-01T00:00:00Z", Status: "active",
		Tiers: []FeeTier{{TierIndex: 0, MinVolume: amt(0), MaxVolume: nil, MakerBps: 2, TakerBps: 5}},
	}
	return Document{
		SchemaVersion: SchemaVersion,
		Assets:        []Asset{assetBTC, assetUSDT},
		Networks:      []Network{network},
		AssetNetworks: []AssetNetwork{mapping},
		Instruments: []Instrument{
			{
				SymbolID: 1, Symbol: "BTCUSDT", Kind: "SPOT", Base: 1, Quote: 2,
				TickSize: 100_000, LotSize: 100_000, PriceDecimals: 1, QtyDecimals: 5,
				MinQty: 1, MaxQty: 10_000, MinNotional: amt(5_000_000), MaxNotional: amt(1_000_000_000_000),
				PriceBandBps: 500, MakerFeeBps: 10, TakerFeeBps: 10, Status: "ACTIVE",
				AllowedOrderTypes: []string{"LIMIT", "MARKET"}, AllowedTimeInForce: []string{"GTC", "IOC"},
				ListedAt: "2026-01-01T00:00:00Z", FeeScheduleID: "spot-default",
			},
			{
				SymbolID: 2, Symbol: "BTCUSDT-PERP", Kind: "PERPETUAL", Base: 1, Quote: 2,
				TickSize: 100_000, LotSize: 100_000, PriceDecimals: 1, QtyDecimals: 5,
				MinQty: 1, MaxQty: 10_000, MinNotional: amt(5_000_000), MaxNotional: amt(1_000_000_000_000),
				PriceBandBps: 500, MakerFeeBps: 2, TakerFeeBps: 5, Status: "ACTIVE",
				Contract: &ContractSpec{
					ContractSize: Multiplier{Numerator: 1, Denominator: 1}, SettlementAsset: 2,
					MaxLeverage: 125, MarginTiers: tiers, FundingIntervalSecs: 28_800,
					FundingCapBps: 75, LiquidationFeeBps: 50, ADLEnabled: true,
				},
				AllowedOrderTypes: []string{"LIMIT", "MARKET", "STOP_MARKET"}, AllowedTimeInForce: []string{"GTC", "IOC"},
				ListedAt: "2026-01-01T00:00:00Z", FeeScheduleID: "perp-default",
				MarginTierSetID:          stringPtr("btc-perp"),
				PriceSourcePolicyRef:     stringPtr("canonical-perp-v1"),
				MarginModeCapabilities:   []string{"cross", "isolated"},
				PositionModeCapabilities: []string{"one_way", "hedge"},
			},
		},
		FeeSchedules: []FeeSchedule{fee, perpFee},
		MarginTierSets: []MarginTierSet{{
			SetID: "btc-perp", Name: "BTC perp tiers", MarketType: "perp",
			EffectiveAt: "2026-01-01T00:00:00Z", Status: "active", MaxLeverage: 125, Tiers: tiers,
		}},
		PresentationPolicies: []PresentationPolicy{{
			PolicyID: "btcusdt-presentation", Symbol: "BTCUSDT", SourcePriority: []string{"bybit", "mexc"},
			FallbackPriority: []string{"nexora_index"}, FreshnessMS: 1_000, StalenessMS: 3_000,
			MaxDeviationBps: 50, QuoteMethod: "midpoint", MaxQuoteOffsetBps: 10,
			AuthoritativeForRisk: false, Status: "active",
		}},
	}
}

func TestValidDocumentPasses(t *testing.T) {
	document := validDocument()
	if issues := document.Validate(); len(issues) > 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	encoded, err := document.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical bytes: %v", err)
	}
	hash := ContentHash(encoded)
	if len(hash) != 64 {
		t.Fatalf("unexpected hash %q", hash)
	}
	if err := VerifyHash(encoded, hash); err != nil {
		t.Fatalf("verify hash: %v", err)
	}
}

func TestCanonicalBytesAreDeterministicAndOrdered(t *testing.T) {
	one := validDocument()
	two := validDocument()
	two.Assets[0], two.Assets[1] = two.Assets[1], two.Assets[0]
	two.Instruments[0], two.Instruments[1] = two.Instruments[1], two.Instruments[0]
	oneBytes, err := one.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	twoBytes, err := two.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(oneBytes) != string(twoBytes) {
		t.Fatal("canonical bytes depend on input ordering")
	}
	if ContentHash(oneBytes) != ContentHash(twoBytes) {
		t.Fatal("content hash depends on input ordering")
	}
}

func TestAmountSupportsOneHundredTwentyEightBitValues(t *testing.T) {
	large := MustAmount("1000000000000000000000") // 1000 ETH in atomic units
	encoded, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "1000000000000000000000" {
		t.Fatalf("unexpected amount encoding %s", encoded)
	}
	parsed, err := ParseAmount(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(large) {
		t.Fatal("large amount did not round-trip")
	}
	var decoded Amount
	if err := json.Unmarshal([]byte("1e21"), &decoded); err == nil {
		t.Fatal("exponent notation was accepted")
	}
	if err := json.Unmarshal([]byte("\"1000\""), &decoded); err == nil {
		t.Fatal("string amount was accepted")
	}
}

func TestValidationRejectsInvalidDocuments(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Document)
		code   string
	}{
		{"invalid precision", func(d *Document) { d.Assets[0].Decimals = 19 }, "REF_RANGE"},
		{"duplicate asset id", func(d *Document) { d.Assets[1].AssetID = d.Assets[0].AssetID }, "REF_DUPLICATE"},
		{"duplicate asset symbol", func(d *Document) { d.Assets[1].Symbol = d.Assets[0].Symbol }, "REF_DUPLICATE"},
		{"unknown mapping network", func(d *Document) { d.AssetNetworks[0].NetworkID = "missing" }, "REF_UNKNOWN_REF"},
		{"mapping decimals exceed asset", func(d *Document) { d.AssetNetworks[0].Decimals = 9 }, "REF_RELATION"},
		{"native contract address", func(d *Document) {
			d.AssetNetworks[0].ContractAddress = stringPtr("0x0000000000000000000000000000000000000001")
		}, "REF_RELATION"},
		{"misaligned tick", func(d *Document) { d.Instruments[0].TickSize = 3 }, "REF_ALIGNMENT"},
		{"misaligned lot", func(d *Document) { d.Instruments[0].LotSize = 3 }, "REF_ALIGNMENT"},
		{"min above max qty", func(d *Document) { d.Instruments[0].MinQty = 20_000 }, "REF_RANGE"},
		{"min above max notional", func(d *Document) { d.Instruments[0].MaxNotional = amt(1) }, "REF_RANGE"},
		{"spot contract", func(d *Document) { d.Instruments[0].Contract = d.Instruments[1].Contract }, "REF_RELATION"},
		{"perp without contract", func(d *Document) { d.Instruments[1].Contract = nil }, "REF_REQUIRED"},
		{"perp settlement mismatch", func(d *Document) { d.Instruments[1].Contract.SettlementAsset = 1 }, "REF_RELATION"},
		{"fee tier gap", func(d *Document) {
			d.FeeSchedules[0].Tiers[0].MaxVolume = amtPtr(5)
			d.FeeSchedules[0].Tiers = append(d.FeeSchedules[0].Tiers, FeeTier{TierIndex: 1, MinVolume: amt(10), MakerBps: 5, TakerBps: 5})
		}, "REF_TIER_GAP"},
		{"negative taker fee", func(d *Document) { d.FeeSchedules[0].Tiers[0].TakerBps = -1 }, "REF_RANGE"},
		{"negative maker without rebate", func(d *Document) { d.FeeSchedules[0].Tiers[0].MakerBps = -1 }, "REF_RELATION"},
		{"margin gap", func(d *Document) {
			d.Instruments[1].Contract.MarginTiers[1].NotionalFloor = d.Instruments[1].Contract.MarginTiers[1].NotionalFloor.Add(amt(1))
			d.MarginTierSets[0].Tiers[1].NotionalFloor = d.MarginTierSets[0].Tiers[1].NotionalFloor.Add(amt(1))
		}, "REF_TIER_GAP"},
		{"margin overlap", func(d *Document) {
			d.Instruments[1].Contract.MarginTiers[1].NotionalFloor = d.Instruments[1].Contract.MarginTiers[1].NotionalFloor.Sub(amt(1))
			d.MarginTierSets[0].Tiers[1].NotionalFloor = d.MarginTierSets[0].Tiers[1].NotionalFloor.Sub(amt(1))
		}, "REF_TIER_OVERLAP"},
		{"margin maintenance mismatch", func(d *Document) {
			d.Instruments[1].Contract.MarginTiers[1].MaintenanceAmount = d.Instruments[1].Contract.MarginTiers[1].MaintenanceAmount.Add(amt(1))
			d.MarginTierSets[0].Tiers[1].MaintenanceAmount = d.MarginTierSets[0].Tiers[1].MaintenanceAmount.Add(amt(1))
		}, "REF_TIER_RELATION"},
		{"margin leverage increase", func(d *Document) {
			d.Instruments[1].Contract.MarginTiers[1].MaxLeverage = 126
			d.MarginTierSets[0].Tiers[1].MaxLeverage = 126
		}, "REF_TIER_ORDER"},
		{"invalid effective timestamp", func(d *Document) { d.FeeSchedules[0].EffectiveAt = "not-a-time" }, "REF_TIMESTAMP"},
		{"presentation risk authority", func(d *Document) { d.PresentationPolicies[0].AuthoritativeForRisk = true }, "REF_RELATION"},
		{"synthetic volume unlabelled", func(d *Document) { d.PresentationPolicies[0].ShowSyntheticVolume = true }, "REF_RELATION"},
		{"unknown schema", func(d *Document) { d.SchemaVersion = "nexora.reference-data.v2" }, "REF_SCHEMA"},
		{"perpetual max above tier cap", func(d *Document) { d.Instruments[1].MaxNotional = amt(2_000_000_000_000) }, "REF_RELATION"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := validDocument()
			testCase.mutate(&document)
			issues := document.Validate()
			if len(issues) == 0 {
				t.Fatal("expected validation failure")
			}
			found := false
			for _, issue := range issues {
				if issue.Code == testCase.code {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected code %s, got %+v", testCase.code, issues)
			}
		})
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	document := validDocument()
	encoded, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	var mutated map[string]any
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated["unexpected"] = true
	raw, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestConsumerRejectsBadHashAndStaleVersion(t *testing.T) {
	document := validDocument()
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash := ContentHash(canonical)
	consumer := NewConsumer(nil)

	bad := Snapshot{Version: 1, ContentHash: strings.Repeat("0", 64), Document: document, Canonical: canonical}
	if err := consumer.Apply(bad, ApplyOptions{}); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected hash mismatch, got %v", err)
	}

	first := Snapshot{Version: 2, ContentHash: hash, Document: document, Canonical: canonical, EffectiveAt: time.Now()}
	if err := consumer.Apply(first, ApplyOptions{}); err != nil {
		t.Fatalf("apply first: %v", err)
	}
	if consumer.Current().Version != 2 {
		t.Fatal("first snapshot not installed")
	}

	older := Snapshot{Version: 1, ContentHash: hash, Document: document, Canonical: canonical}
	if err := consumer.Apply(older, ApplyOptions{}); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("expected stale rejection, got %v", err)
	}
	if err := consumer.Apply(older, ApplyOptions{AllowRollback: true}); err != nil {
		t.Fatalf("explicit rollback rejected: %v", err)
	}
	if consumer.Current().Version != 1 {
		t.Fatal("rollback snapshot not installed")
	}
	applied, rejected := consumer.Stats()
	if applied != 2 || rejected < 2 {
		t.Fatalf("unexpected stats applied=%d rejected=%d", applied, rejected)
	}
}

func TestConsumerRejectsConflictingHashForSameVersion(t *testing.T) {
	document := validDocument()
	canonical, _ := document.CanonicalBytes()
	hash := ContentHash(canonical)
	consumer := NewConsumer(nil)
	if err := consumer.Apply(Snapshot{Version: 1, ContentHash: hash, Document: document, Canonical: canonical}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	conflicting := validDocument()
	conflicting.Assets[0].DisplayName = "Different"
	otherBytes, _ := conflicting.CanonicalBytes()
	err := consumer.Apply(Snapshot{Version: 1, ContentHash: ContentHash(otherBytes), Document: conflicting, Canonical: otherBytes}, ApplyOptions{})
	if err == nil {
		t.Fatal("conflicting hash for the same version was accepted")
	}
}
