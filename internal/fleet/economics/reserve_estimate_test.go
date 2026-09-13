package economics

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
)

// TestTokensOutDefaultAgainstGolden pins all nine R-21.35/R-21.50
// defaults against testdata/goldens/tokens_out_defaults.json, authored
// from the ruling text (see testdata/README.md for provenance).
func TestTokensOutDefaultAgainstGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/goldens/tokens_out_defaults.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]int64
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(golden) != 9 {
		t.Fatalf("golden has %d entries, want 9", len(golden))
	}
	for class, want := range golden {
		got, err := TokensOutDefault(conductor.TaskClass(class))
		if err != nil {
			t.Errorf("TokensOutDefault(%q): %v", class, err)
			continue
		}
		if got != want {
			t.Errorf("TokensOutDefault(%q) = %d, want %d (golden)", class, got, want)
		}
	}
}

func TestTokensOutDefaultUnknownClass(t *testing.T) {
	for _, bad := range []conductor.TaskClass{"", "triage", "generate"} {
		if _, err := TokensOutDefault(bad); err == nil {
			t.Errorf("TokensOutDefault(%q) = nil error, want ErrUnknownTaskClass", bad)
		}
	}
}

func TestEstimateForCeilBoundary(t *testing.T) {
	// hydrated=10 -> 10*1.10=11.0 exactly, no rounding needed.
	est, err := EstimateFor(10, conductor.TaskClassChat, 1)
	if err != nil {
		t.Fatalf("EstimateFor: %v", err)
	}
	if est.TokensIn != 11 {
		t.Errorf("TokensIn = %d, want 11 (exact boundary)", est.TokensIn)
	}
	// hydrated=9 -> 9*1.10=9.9 -> ceil = 10 (fractional boundary).
	est2, err := EstimateFor(9, conductor.TaskClassChat, 1)
	if err != nil {
		t.Fatalf("EstimateFor: %v", err)
	}
	if est2.TokensIn != 10 {
		t.Errorf("TokensIn = %d, want 10 (ceil of 9.9)", est2.TokensIn)
	}
	if est2.TokensOut != 4096 {
		t.Errorf("TokensOut = %d, want 4096 (chat default)", est2.TokensOut)
	}
	if est2.Requests != 1 {
		t.Errorf("Requests = %d, want 1", est2.Requests)
	}
}

func TestEstimateForRejectsNegativeHydratedSize(t *testing.T) {
	if _, err := EstimateFor(-1, conductor.TaskClassChat, 1); err == nil {
		t.Error("EstimateFor(-1, ...) = nil error, want typed error")
	}
}

func TestEstimateForRejectsSubOneRequests(t *testing.T) {
	if _, err := EstimateFor(100, conductor.TaskClassChat, 0); err == nil {
		t.Error("EstimateFor(..., requests=0) = nil error, want typed error")
	}
	if _, err := EstimateFor(100, conductor.TaskClassChat, -5); err == nil {
		t.Error("EstimateFor(..., requests=-5) = nil error, want typed error")
	}
}

func TestEstimateForUnknownTaskClass(t *testing.T) {
	if _, err := EstimateFor(100, conductor.TaskClass("bogus"), 1); err == nil {
		t.Error("EstimateFor(..., bogus class) = nil error, want ErrUnknownTaskClass")
	}
}
