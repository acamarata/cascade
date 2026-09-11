package nodes

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTrustTierRankGolden asserts the ordered-rank gate table (R-21.220)
// against internal/nodes/testdata/trust/tier_gates.golden: all three
// tiers x the local-only and restricted gates, proving paired-device
// fails the restricted gate.
func TestTrustTierRankGolden(t *testing.T) {
	f, err := os.Open("testdata/trust/tier_gates.golden")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("golden file is empty (missing header)")
	}
	rows := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			t.Fatalf("malformed golden row %q", line)
		}
		tier, gate := Tier(parts[0]), Gate(parts[1])
		want, err := strconv.ParseBool(parts[2])
		if err != nil {
			t.Fatalf("malformed golden bool %q: %v", parts[2], err)
		}
		got := Satisfies(tier, gate)
		if got != want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", tier, gate, got, want)
		}
		rows++
	}
	if rows != 6 {
		t.Fatalf("expected 6 golden rows (3 tiers x 2 gates), got %d", rows)
	}

	// Explicitly assert the R-21.220 headline: paired-device fails the
	// restricted gate.
	if Satisfies(TierPairedDevice, GateRestricted) {
		t.Fatal("paired-device must fail the restricted gate")
	}
}

func TestRank(t *testing.T) {
	cases := []struct {
		tier Tier
		want int
		ok   bool
	}{
		{TierController, 2, true},
		{TierWorkerTrusted, 1, true},
		{TierPairedDevice, 0, true},
		{Tier("bogus"), -1, false},
		{Tier(""), -1, false},
	}
	for _, c := range cases {
		gotRank, gotOK := Rank(c.tier)
		if gotRank != c.want || gotOK != c.ok {
			t.Errorf("Rank(%q) = (%d, %v), want (%d, %v)", c.tier, gotRank, gotOK, c.want, c.ok)
		}
	}
}

func TestRankOrdering(t *testing.T) {
	cRank, _ := Rank(TierController)
	wRank, _ := Rank(TierWorkerTrusted)
	pRank, _ := Rank(TierPairedDevice)
	if cRank <= wRank || wRank <= pRank {
		t.Fatalf("expected controller > worker-trusted > paired-device ranks, got %d, %d, %d", cRank, wRank, pRank)
	}
}

func TestValidateTier(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		allowPaired bool
		wantErr     bool
		wantKind    cascade.Kind
	}{
		{"empty is fail-closed, never defaulted", "", false, true, cascade.KindInvalidInput},
		{"unknown value refused", "super-admin", false, true, cascade.KindInvalidInput},
		{"controller ok", "controller", false, false, 0},
		{"worker-trusted ok", "worker-trusted", false, false, 0},
		{"paired-device refused on enroll path", "paired-device", false, true, cascade.KindInvalidInput},
		{"paired-device ok when allowed", "paired-device", true, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateTier(c.raw, c.allowPaired)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if k, ok := cascade.KindOf(err); !ok || k != c.wantKind {
					t.Fatalf("expected kind %v, got %v (ok=%v)", c.wantKind, k, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != c.raw {
				t.Fatalf("got %q, want %q", got, c.raw)
			}
		})
	}
}

func TestSatisfiesUnknownGateRefused(t *testing.T) {
	if Satisfies(TierController, Gate("bogus-gate")) {
		t.Fatal("an unrecognized gate must never be satisfied")
	}
}
