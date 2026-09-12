package topology

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type laneClassGoldenRow struct {
	Class           string  `json:"class"`
	BaseShadowPrice float64 `json:"base_shadow_price"`
}

// TestBaseShadowPriceGolden asserts BaseShadowPrice byte-for-byte against
// testdata/lane_class_prices.golden.json, in the R-21.30 declaration order.
func TestBaseShadowPriceGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/lane_class_prices.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []laneClassGoldenRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(rows) != len(orderedLaneClasses) {
		t.Fatalf("golden has %d rows, want %d (seventeen R-21.30 classes)", len(rows), len(orderedLaneClasses))
	}
	for i, class := range orderedLaneClasses {
		if rows[i].Class != string(class) {
			t.Fatalf("row %d: golden class %q, want %q (order must match R-21.30)", i, rows[i].Class, class)
		}
		got, err := BaseShadowPrice(class)
		if err != nil {
			t.Fatalf("BaseShadowPrice(%q): %v", class, err)
		}
		if got != rows[i].BaseShadowPrice {
			t.Errorf("BaseShadowPrice(%q) = %v, golden wants %v", class, got, rows[i].BaseShadowPrice)
		}
	}
}

func TestBaseShadowPriceUnknownClass(t *testing.T) {
	_, err := BaseShadowPrice(LaneClass("executive-overflow"))
	if err == nil {
		t.Fatal("BaseShadowPrice(unknown class) should have failed")
	}
	if !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("error should wrap ErrTopologyInvariant, got %v", err)
	}
}

func TestLaneClassValid(t *testing.T) {
	for _, c := range orderedLaneClasses {
		if !c.Valid() {
			t.Errorf("LaneClass %q should be valid", c)
		}
	}
	if LaneClass("").Valid() {
		t.Error("zero-value LaneClass should be invalid")
	}
	if LaneClass("executive-overflow").Valid() {
		t.Error("executive-overflow is a DERIVED effective class (R-21.45), never a LaneClass member")
	}
}

func TestLaneClassString(t *testing.T) {
	if LaneClassCritic.String() != "critic" {
		t.Errorf("String() = %q, want \"critic\"", LaneClassCritic.String())
	}
}
