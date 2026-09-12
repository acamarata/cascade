package economics

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// TestShadowPriceResultZeroValue asserts ShadowPriceResult's zero value:
// every numeric term at 0 and both signals false, so an accidentally
// unset result never reads as eligible or priced.
func TestShadowPriceResultZeroValue(t *testing.T) {
	var got ShadowPriceResult
	want := ShadowPriceResult{}
	if got != want {
		t.Fatalf("zero ShadowPriceResult = %+v, want %+v", got, want)
	}
	if got.BelowHardReserve || got.BelowAbsoluteFloor {
		t.Fatal("zero ShadowPriceResult must not read as below any reserve signal")
	}
}

// TestShadowPriceInputJSONRoundTrip asserts ShadowPriceInput survives a
// marshal/unmarshal cycle byte-for-value, including its embedded
// topology.QuotaDomain.
func TestShadowPriceInputJSONRoundTrip(t *testing.T) {
	in := ShadowPriceInput{
		LaneID:            topology.LaneID("lane-1"),
		BasePrice:         2.5,
		ModeMultiplier:    1.2,
		Reserve:           0.2,
		RemainingFraction: 0.5,
		Domain: topology.QuotaDomain{
			ID:   topology.DomainID("dom-1"),
			Kind: topology.QuotaDomainSharedPool,
			Dimensions: map[string]topology.Bucket{
				topology.DimensionWeekly: {
					Name:              topology.DimensionWeekly,
					Source:            topology.SourceCLIObservation,
					RemainingFraction: 0.5,
					CapacityObserved:  topology.UnobservedCapacity,
				},
			},
		},
		Ewma429: 0.1,
		Now:     time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ShadowPriceInput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Now.Equal(in.Now) {
		t.Fatalf("Now round-trip = %v, want %v", got.Now, in.Now)
	}
	got.Now = in.Now // time.Time equality via == can differ by monotonic reading
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round-trip = %+v, want %+v", got, in)
	}
}
