package economics

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// shadowPriceGoldenRow mirrors one row of
// testdata/goldens/shadow_price_table.json: every input ShadowPrice needs,
// resolved by the caller, so the test can independently recompute each
// term from the real R-21.30/R-21.31/R-21.53 functions and assert the
// composed formula, never a second copy of any table.
type shadowPriceGoldenRow struct {
	Case                    string  `json:"case"`
	LaneClass               string  `json:"lane_class"`
	ModeMultiplier          float64 `json:"mode_multiplier"`
	Reserve                 float64 `json:"reserve"`
	RemainingFraction       float64 `json:"remaining_fraction"`
	Dimension               string  `json:"dimension"`
	DomainRemainingFraction float64 `json:"domain_remaining_fraction"`
	ResetInSeconds          float64 `json:"reset_in_seconds"`
	Ewma429                 float64 `json:"ewma429"`
}

func loadShadowPriceGoldens(t *testing.T) []shadowPriceGoldenRow {
	t.Helper()
	data, err := os.ReadFile("testdata/goldens/shadow_price_table.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []shadowPriceGoldenRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	return rows
}

// domainFromRow builds the single-dimension QuotaDomain row describes: a
// rolling bucket (rpm) driven entirely by RemainingFraction, or a fixed
// bucket (rpd) whose ResetAt is ResetInSeconds from now -- the same two
// shapes AN/S-78.T1's own chooser_pressure_test.go golden uses.
func domainFromRow(row shadowPriceGoldenRow, now time.Time) topology.QuotaDomain {
	b := topology.Bucket{
		Name:              row.Dimension,
		Source:            topology.SourceCLIObservation,
		RemainingFraction: row.DomainRemainingFraction,
		CapacityObserved:  topology.UnobservedCapacity,
	}
	if row.Dimension == topology.DimensionRPD {
		b.ResetAt = now.Add(time.Duration(row.ResetInSeconds) * time.Second)
	}
	return topology.QuotaDomain{
		ID:         topology.DomainID("d-" + row.Case),
		Dimensions: map[string]topology.Bucket{row.Dimension: b},
	}
}

func almostEqualPrice(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestShadowPriceGoldenTable asserts, for every R-21.30 lane class,
// ShadowPrice equals BasePrice (drawn from topology.BaseShadowPrice, the
// real R-21.30 table, never a second copy) times topology.DomainPressure
// (computed independently here, proving pass-through with no local
// recomputation) times ModeMultiplier times ReserveBarrier (computed
// independently here), and that every term is exposed on the result.
func TestShadowPriceGoldenTable(t *testing.T) {
	now := newTestClock().Now()
	for _, row := range loadShadowPriceGoldens(t) {
		row := row
		t.Run(row.Case, func(t *testing.T) {
			basePrice, err := topology.BaseShadowPrice(topology.LaneClass(row.LaneClass))
			if err != nil {
				t.Fatalf("BaseShadowPrice(%s): %v", row.LaneClass, err)
			}
			domain := domainFromRow(row, now)
			in := ShadowPriceInput{
				LaneID:            topology.LaneID(row.Case),
				BasePrice:         basePrice,
				ModeMultiplier:    row.ModeMultiplier,
				Reserve:           row.Reserve,
				RemainingFraction: row.RemainingFraction,
				Domain:            domain,
				Ewma429:           row.Ewma429,
				Now:               now,
			}
			got := ShadowPrice(in)
			assertShadowPriceRow(t, row, domain, basePrice, now, got)
		})
	}
}

func assertShadowPriceRow(t *testing.T, row shadowPriceGoldenRow, domain topology.QuotaDomain, basePrice float64, now time.Time, got ShadowPriceResult) {
	t.Helper()
	wantPressure := topology.DomainPressure(domain, row.Reserve, row.Ewma429, now)
	wantBarrier, wantAtOrBelow, wantBelowFloor := ReserveBarrier(row.RemainingFraction, row.Reserve)
	wantPrice := basePrice * wantPressure * row.ModeMultiplier * wantBarrier
	if got.Base != basePrice {
		t.Errorf("Base = %v, want %v", got.Base, basePrice)
	}
	if got.Pressure != wantPressure {
		t.Errorf("Pressure = %v, want topology.DomainPressure = %v (no local recomputation allowed)", got.Pressure, wantPressure)
	}
	if got.ModeMultiplier != row.ModeMultiplier {
		t.Errorf("ModeMultiplier = %v, want %v", got.ModeMultiplier, row.ModeMultiplier)
	}
	if got.ReserveBarrier != wantBarrier {
		t.Errorf("ReserveBarrier = %v, want %v", got.ReserveBarrier, wantBarrier)
	}
	if !almostEqualPrice(got.ShadowPrice, wantPrice) {
		t.Errorf("ShadowPrice = %v, want Base*Pressure*ModeMultiplier*ReserveBarrier = %v", got.ShadowPrice, wantPrice)
	}
	if got.BelowHardReserve != wantAtOrBelow {
		t.Errorf("BelowHardReserve = %v, want %v", got.BelowHardReserve, wantAtOrBelow)
	}
	if got.BelowAbsoluteFloor != wantBelowFloor {
		t.Errorf("BelowAbsoluteFloor = %v, want %v", got.BelowAbsoluteFloor, wantBelowFloor)
	}
}

// TestReserveBarrierBoundaries asserts the R-21.31 three-region barrier
// value at and around reserve=0.2's boundaries. Every case is a clean
// fraction whose exact value is asserted to float64 rounding tolerance
// (1e-9) -- IEEE754 addition of 0.2+0.1 is not bit-exact, so the
// tolerance absorbs float representation error only, never a formula
// error of that magnitude.
func TestReserveBarrierBoundaries(t *testing.T) {
	const reserve = 0.2
	cases := []struct {
		name              string
		remainingFraction float64
		wantFactor        float64
		wantAtOrBelow     bool
	}{
		{"above-band-abundant", 0.31, 1.0, false},
		{"at-upper-band-boundary", 0.30, 1.0, false},
		{"band-midpoint", 0.25, 2.5, false},
		{"at-reserve-hard-boundary", 0.20, ReserveBarrierMax, true},
		{"below-reserve-hard", 0.10, ReserveBarrierMax, true},
	}
	for _, c := range cases {
		factor, atOrBelow, _ := ReserveBarrier(c.remainingFraction, reserve)
		if !almostEqualPrice(factor, c.wantFactor) {
			t.Errorf("%s: factor = %v, want %v", c.name, factor, c.wantFactor)
		}
		if atOrBelow != c.wantAtOrBelow {
			t.Errorf("%s: atOrBelowReserve = %v, want %v", c.name, atOrBelow, c.wantAtOrBelow)
		}
	}
}

// TestReserveBarrierAbsoluteFloor asserts R-21.118's independent third
// signal: exact at its 0.5*reserve boundary, and a case where it differs
// from atOrBelowReserve (the two signals are not the same one renamed).
func TestReserveBarrierAbsoluteFloor(t *testing.T) {
	const reserve = 0.2
	cases := []struct {
		name              string
		remainingFraction float64
		wantAtOrBelow     bool
		wantBelowFloor    bool
	}{
		{"at-floor-boundary-not-below", 0.10, true, false},
		{"just-below-floor", 0.09, true, true},
		{"between-floor-and-reserve-signals-differ", 0.15, true, false},
		{"well-above-everything", 1.0, false, false},
	}
	for _, c := range cases {
		_, atOrBelow, belowFloor := ReserveBarrier(c.remainingFraction, reserve)
		if atOrBelow != c.wantAtOrBelow {
			t.Errorf("%s: atOrBelowReserve = %v, want %v", c.name, atOrBelow, c.wantAtOrBelow)
		}
		if belowFloor != c.wantBelowFloor {
			t.Errorf("%s: belowAbsoluteFloor = %v, want %v", c.name, belowFloor, c.wantBelowFloor)
		}
	}
	if _, atOrBelow, belowFloor := ReserveBarrier(0.15, reserve); atOrBelow == belowFloor {
		t.Fatalf("at 0.15: atOrBelowReserve (%v) and belowAbsoluteFloor (%v) must differ -- they are independent signals, not the same one twice", atOrBelow, belowFloor)
	}
}

// unknownPressureGoldenRow mirrors
// testdata/goldens/unknown_pressure_table.json.
type unknownPressureGoldenRow struct {
	Case              string  `json:"case"`
	LaneClass         string  `json:"lane_class"`
	ModeMultiplier    float64 `json:"mode_multiplier"`
	Reserve           float64 `json:"reserve"`
	RemainingFraction float64 `json:"remaining_fraction"`
	Ewma429Zero       float64 `json:"ewma429_zero"`
	Ewma429Sustained  float64 `json:"ewma429_sustained"`
}

// TestShadowPriceUnknownBucketPressure asserts the R-21.128 unknown-source
// behaviour as received from topology.BucketPressure: a lane whose only
// bucket is unknown-source prices strictly above the same lane at
// ewma_429 zero, is never priced at a flat pressure of 1.0 under sustained
// 429s, and is never treated as unlimited (its pressure stays finite and
// bounded).
func TestShadowPriceUnknownBucketPressure(t *testing.T) {
	data, err := os.ReadFile("testdata/goldens/unknown_pressure_table.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []unknownPressureGoldenRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	now := newTestClock().Now()
	for _, row := range rows {
		row := row
		t.Run(row.Case, func(t *testing.T) {
			runUnknownPressureCase(t, row, now)
		})
	}
}

func runUnknownPressureCase(t *testing.T, row unknownPressureGoldenRow, now time.Time) {
	t.Helper()
	basePrice, err := topology.BaseShadowPrice(topology.LaneClass(row.LaneClass))
	if err != nil {
		t.Fatalf("BaseShadowPrice(%s): %v", row.LaneClass, err)
	}
	domain := topology.QuotaDomain{
		ID: topology.DomainID("d-" + row.Case),
		Dimensions: map[string]topology.Bucket{
			topology.DimensionRPM: {Name: topology.DimensionRPM, Source: topology.SourceUnknown},
		},
	}
	base := ShadowPriceInput{
		BasePrice: basePrice, ModeMultiplier: row.ModeMultiplier,
		Reserve: row.Reserve, RemainingFraction: row.RemainingFraction,
		Domain: domain, Now: now,
	}
	zero, sustained := base, base
	zero.Ewma429 = row.Ewma429Zero
	sustained.Ewma429 = row.Ewma429Sustained
	gotZero := ShadowPrice(zero)
	gotSustained := ShadowPrice(sustained)
	if gotZero.Pressure != 1.0 {
		t.Fatalf("no-429-history pressure = %v, want the R-21.128 floor 1.0", gotZero.Pressure)
	}
	if !(gotSustained.Pressure > gotZero.Pressure) {
		t.Fatalf("sustained-429 pressure %v did not exceed no-history pressure %v", gotSustained.Pressure, gotZero.Pressure)
	}
	if gotSustained.Pressure == 1.0 {
		t.Fatal("sustained-429 lane priced at a flat pressure of 1.0 -- the superseded rule, not R-21.128")
	}
	if gotSustained.Pressure > 25.0 {
		t.Fatalf("pressure %v exceeded the fail-closed clamp max -- never unlimited", gotSustained.Pressure)
	}
	if !(gotSustained.ShadowPrice > gotZero.ShadowPrice) {
		t.Fatalf("sustained-429 shadow price %v did not exceed no-history shadow price %v -- the pressure change never reached the price", gotSustained.ShadowPrice, gotZero.ShadowPrice)
	}
}
