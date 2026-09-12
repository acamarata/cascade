package topology

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// goldenRow mirrors one row of testdata/chooser_scores.golden.json.
type goldenRow struct {
	Case              string  `json:"case"`
	Dimension         string  `json:"dimension"`
	Reserve           float64 `json:"reserve"`
	TTRSeconds        float64 `json:"ttr_seconds"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ActualRemaining   float64 `json:"actual_remaining"`
	Source            string  `json:"source"`
	Ewma429           float64 `json:"ewma_429"`
	ExpectedPressure  float64 `json:"expected_pressure"`
}

func loadGoldenRows(t *testing.T) []goldenRow {
	t.Helper()
	data, err := os.ReadFile("testdata/chooser_scores.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []goldenRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	return rows
}

// bucketFromRow builds the Bucket a golden row describes. For a fixed
// window (rpd), ActualRemaining/TTRSeconds are independent (ResetAt drives
// TimeToReset). For a rolling window (rpm), RemainingFraction alone drives
// both TimeToReset and RemainingFraction (window_map.go's own coupling).
func bucketFromRow(row goldenRow, now time.Time) Bucket {
	b := Bucket{
		Name:              row.Dimension,
		Limit:             1000,
		Source:            BucketSource(row.Source),
		CapacityObserved:  UnobservedCapacity,
		RemainingFraction: row.RemainingFraction,
	}
	if row.Source == "unknown" {
		return b
	}
	window, _, err := WindowFor(row.Dimension)
	if err != nil {
		panic(err)
	}
	if window == BucketWindowRolling {
		return b
	}
	b.RemainingFraction = row.ActualRemaining
	b.ResetAt = now.Add(time.Duration(row.TTRSeconds) * time.Second)
	return b
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// TestDomainPressureGoldens pins DomainPressure/BucketPressure against the
// six R-21.31/R-21.128 table cases (ahead of budget, behind budget, near
// reset, rolling window, unknown-source with no 429 history, unknown-
// source under sustained 429), each carrying its own reserve input.
func TestDomainPressureGoldens(t *testing.T) {
	now := newTestClock().Now()
	for _, row := range loadGoldenRows(t) {
		row := row
		t.Run(row.Case, func(t *testing.T) {
			b := bucketFromRow(row, now)
			got := BucketPressure(b, row.Reserve, row.Ewma429, now)
			if !almostEqual(got, row.ExpectedPressure) {
				t.Fatalf("BucketPressure(%s) = %v, want %v", row.Case, got, row.ExpectedPressure)
			}
			d := QuotaDomain{ID: "d1", Dimensions: map[string]Bucket{row.Dimension: b}}
			gotDomain := DomainPressure(d, row.Reserve, row.Ewma429, now)
			if !almostEqual(gotDomain, row.ExpectedPressure) {
				t.Fatalf("DomainPressure(%s) = %v, want %v", row.Case, gotDomain, row.ExpectedPressure)
			}
		})
	}
}

// TestUnknownBucketPressureRisesWith429 asserts R-21.128: an unknown-source
// bucket's pressure strictly increases with its scope's ewma_429, and is
// never treated as infinite capacity (it never returns below the floor).
func TestUnknownBucketPressureRisesWith429(t *testing.T) {
	now := newTestClock().Now()
	b := Bucket{Name: DimensionRPM, Source: SourceUnknown, RemainingFraction: 1}
	low := BucketPressure(b, 0.2, 0, now)
	high := BucketPressure(b, 0.2, 0.9, now)
	if low != unknownPressureFloor {
		t.Fatalf("no-history pressure = %v, want floor %v", low, unknownPressureFloor)
	}
	if !(high > low) {
		t.Fatalf("sustained-429 pressure %v did not exceed no-history pressure %v", high, low)
	}
	if high > pressureMax {
		t.Fatalf("pressure %v exceeded clamp max %v", high, pressureMax)
	}
}

// TestGaugesGateEligibilityOnly asserts a batch gauge name is refused by
// WindowFor (window_map.go) and therefore never reaches DomainPressure via
// the Dimensions map -- BucketPressure on an unroutable name fails closed
// to the max rather than a silent zero.
func TestGaugesGateEligibilityOnly(t *testing.T) {
	if _, _, err := WindowFor(GaugeConcurrentRequests); err == nil {
		t.Fatal("expected WindowFor to refuse a batch gauge name")
	}
	b := Bucket{Name: GaugeConcurrentRequests, Source: SourceCLIObservation, RemainingFraction: 1}
	if got := BucketPressure(b, 0.2, 0, newTestClock().Now()); got != pressureMax {
		t.Fatalf("gauge-named bucket pressure = %v, want fail-closed max %v", got, pressureMax)
	}
}

// TestWindowLengthFromWindowForMap asserts no dimension can yield a zero
// or absent window (R-21.116): every real dimension name resolves, and an
// unmapped name is refused.
func TestWindowLengthFromWindowForMap(t *testing.T) {
	for _, dim := range []string{DimensionRPM, DimensionTPM, DimensionRPD, DimensionSession5h, DimensionMonthly} {
		_, length, err := WindowFor(dim)
		if err != nil {
			t.Fatalf("WindowFor(%s): %v", dim, err)
		}
		if length <= 0 {
			t.Fatalf("WindowFor(%s) returned non-positive length %v", dim, length)
		}
	}
	if _, _, err := WindowFor("not-a-real-dimension"); err == nil {
		t.Fatal("expected an unmapped dimension name to be refused")
	}
}
