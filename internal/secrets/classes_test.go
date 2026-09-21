// Package secrets_test (EXTERNAL/black-box test package, deliberately —
//   see doc comment below).
//
// Purpose: TestEgressClassBridgeRegistered — the check this ticket's
//   contract names under ./internal/secrets/... (check #14). CONTRACT-
//   VS-TREE NOTE (LANE-RULES §1, quoting both sides): the ticket's own
//   R-35 amendment says the EgressClassBridge entry is "registered at
//   internal/secrets package init (H/S-16.T1 owns RegisterClass)" — but
//   internal/hooks/egress/classes.go's own EgressClassCIPoll comment
//   already documents that "internal/secrets/classes.go... does not
//   exist in this tree; the real registry... lives here [egress]",
//   because RegisterClass/Register/MustRegister are methods on
//   internal/hooks/egress.Registry, and internal/secrets never imported
//   that package before this file. This test does what the check
//   command can literally run (`go test -run
//   TestEgressClassBridgeRegistered ./internal/secrets/...`) while
//   asserting against the REAL registration site,
//   egress.DefaultRegistry(), exactly like every other landed class.
//
//   EXTERNAL PACKAGE, ON PURPOSE: internal/hooks/egress's own
//   intercept.go imports internal/secrets (for its Detector type), so an
//   INTERNAL test file (`package secrets`) importing egress back would
//   put the compiler in the position of needing two variants of this
//   package to satisfy one test binary. `package secrets_test` avoids
//   that entirely — `go test ./internal/secrets/...` compiles and runs
//   external test files in the same package directory, so the check
//   command is unaffected, and this file needs no unexported
//   internal/secrets symbol anyway.
// SPORT: internal/secrets TestEgressClassBridgeRegistered/ADDED
//   (P1-E23-W5-S48-T1).

package secrets_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

func TestEgressClassBridgeRegistered(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassBridge)
	if !ok {
		t.Fatal("egress.EgressClassBridge is not registered in the default registry")
	}
	if !cfg.Enabled {
		t.Fatal("EgressClassBridge.Enabled = false, want true")
	}
	if cfg.AllowRestricted {
		t.Fatal("EgressClassBridge.AllowRestricted = true, want false")
	}
	if cfg.AllowLocalOnly {
		t.Fatal("EgressClassBridge.AllowLocalOnly = true, want false")
	}
	wantTiers := map[egress.SensitivityTier]bool{egress.TierInternal: true, egress.TierPublic: true}
	if len(cfg.AllowedTiers) != len(wantTiers) {
		t.Fatalf("AllowedTiers = %v, want exactly {internal, public}", cfg.AllowedTiers)
	}
	for _, tier := range cfg.AllowedTiers {
		if !wantTiers[tier] {
			t.Fatalf("AllowedTiers contains unexpected tier %q", tier)
		}
	}
	if cfg.Owner != "P1-E23-W5-S48-T1" {
		t.Fatalf("Owner = %q, want P1-E23-W5-S48-T1", cfg.Owner)
	}
}

func TestEgressClassBridge_StringValue(t *testing.T) {
	if string(egress.EgressClassBridge) != "bridge" {
		t.Fatalf("EgressClassBridge = %q, want \"bridge\"", string(egress.EgressClassBridge))
	}
}
