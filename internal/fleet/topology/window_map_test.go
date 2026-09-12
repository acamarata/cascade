package topology

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

type windowGoldenRow struct {
	Dimension     string `json:"dimension"`
	Window        string `json:"window"`
	LengthSeconds int64  `json:"length_seconds"`
}

// TestWindowForEveryDimension is the named acceptance test: WindowFor
// covers every permitted dimension per the mandatory R-21.116 map, pinned
// against testdata/window_map.golden.json.
func TestWindowForEveryDimension(t *testing.T) {
	raw, err := os.ReadFile("testdata/window_map.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []windowGoldenRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(rows) != 9 {
		t.Fatalf("golden has %d rows, want 9 (every closed dimension name)", len(rows))
	}
	for _, row := range rows {
		window, length, err := WindowFor(row.Dimension)
		if err != nil {
			t.Errorf("%s: WindowFor error = %v", row.Dimension, err)
			continue
		}
		if string(window) != row.Window {
			t.Errorf("%s: window = %s, want %s", row.Dimension, window, row.Window)
		}
		if length != time.Duration(row.LengthSeconds)*time.Second {
			t.Errorf("%s: length = %s, want %ds", row.Dimension, length, row.LengthSeconds)
		}
	}
}

func TestWindowForUnmappedDimensionIsInvariant(t *testing.T) {
	if _, _, err := WindowFor("nonexistent_dimension"); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("unmapped dimension: err = %v, want ErrTopologyInvariant", err)
	}
}

// TestGaugesAreNotPressureInputs is the named acceptance test: the two
// batch gauges are refused by WindowFor with ErrTopologyInvariant, so no
// caller can feed a gauge into the pressure computation.
func TestGaugesAreNotPressureInputs(t *testing.T) {
	for _, gauge := range []string{GaugeConcurrentRequests, GaugeEnqueuedTokens} {
		if _, _, err := WindowFor(gauge); !errors.Is(err, ErrTopologyInvariant) {
			t.Errorf("WindowFor(%s) err = %v, want ErrTopologyInvariant", gauge, err)
		}
	}
}

func TestTimeToResetRolling(t *testing.T) {
	b := Bucket{Name: DimensionRPM, RemainingFraction: 0.5}
	got, err := TimeToReset(b, time.Now())
	if err != nil {
		t.Fatalf("TimeToReset: %v", err)
	}
	want := 30 * time.Second
	if got != want {
		t.Errorf("TimeToReset(rolling, 0.5) = %v, want %v", got, want)
	}
}

func TestTimeToResetFixedWindowFlooredAtZero(t *testing.T) {
	now := time.Now()
	past := Bucket{Name: DimensionRPD, ResetAt: now.Add(-time.Hour)}
	got, err := TimeToReset(past, now)
	if err != nil {
		t.Fatalf("TimeToReset: %v", err)
	}
	if got != 0 {
		t.Errorf("TimeToReset(stale reset_at) = %v, want 0", got)
	}

	future := Bucket{Name: DimensionRPD, ResetAt: now.Add(2 * time.Hour)}
	got2, err := TimeToReset(future, now)
	if err != nil {
		t.Fatalf("TimeToReset: %v", err)
	}
	if got2 != 2*time.Hour {
		t.Errorf("TimeToReset(future reset_at) = %v, want 2h", got2)
	}
}

func TestTimeToResetPropagatesUnmappedError(t *testing.T) {
	b := Bucket{Name: "bogus"}
	if _, err := TimeToReset(b, time.Now()); err == nil {
		t.Fatal("expected error for unmapped dimension")
	}
}
