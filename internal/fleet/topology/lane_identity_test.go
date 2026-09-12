package topology

import (
	"encoding/json"
	"os"
	"testing"
)

type laneIdentityGoldenRow struct {
	RuntimeProfileRef string `json:"runtime_profile_ref"`
	QuotaDomainRef    string `json:"quota_domain_ref"`
	CanonicalID       string `json:"canonical_id"`
	ModelID           string `json:"model_id"`
	Effort            string `json:"effort"`
	InteractionClass  string `json:"interaction_class"`
	WantLaneID        string `json:"want_lane_id"`
}

// TestLaneIDDerivation pins DeriveLaneID/LaneIDFor against
// testdata/lane_identity.golden.json (R-21.124: first 16 hex of sha256 over
// the five joined components, preferring canonical_id over model_id).
func TestLaneIDDerivation(t *testing.T) {
	data, err := os.ReadFile("testdata/lane_identity.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var rows []laneIdentityGoldenRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("golden file should have at least one row")
	}
	for _, r := range rows {
		got := DeriveLaneID(r.RuntimeProfileRef, r.QuotaDomainRef, canonicalOrModelIDFor(r.CanonicalID, r.ModelID), r.Effort, r.InteractionClass)
		if string(got) != r.WantLaneID {
			t.Errorf("DeriveLaneID(%+v) = %q, want %q", r, got, r.WantLaneID)
		}
		if len(got) != laneIDHexLen {
			t.Errorf("LaneID length = %d, want %d", len(got), laneIDHexLen)
		}
		via := LaneIDFor(RuntimeProfileID(r.RuntimeProfileRef), DomainID(r.QuotaDomainRef), r.CanonicalID, r.ModelID, Effort(r.Effort), InteractionClass(r.InteractionClass))
		if string(via) != r.WantLaneID {
			t.Errorf("LaneIDFor(%+v) = %q, want %q", r, via, r.WantLaneID)
		}
	}
}

func TestCanonicalOrModelIDPrefersCanonical(t *testing.T) {
	if got := canonicalOrModelIDFor("canon", "model"); got != "canon" {
		t.Errorf("canonicalOrModelIDFor should prefer non-empty canonical_id, got %q", got)
	}
	if got := canonicalOrModelIDFor("", "model"); got != "model" {
		t.Errorf("canonicalOrModelIDFor should fall back to model_id when canonical_id is empty, got %q", got)
	}
}

func TestDeriveLaneIDDeterministic(t *testing.T) {
	a := DeriveLaneID("rp", "dom", "canon", "high", "interactive")
	b := DeriveLaneID("rp", "dom", "canon", "high", "interactive")
	if a != b {
		t.Error("DeriveLaneID should be deterministic for identical inputs")
	}
	c := DeriveLaneID("rp", "dom", "canon", "low", "interactive")
	if a == c {
		t.Error("DeriveLaneID should differ when a component changes")
	}
}
