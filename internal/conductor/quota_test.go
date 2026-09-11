package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseQuotaConfig_MissingSection_FailClosedDivergent(t *testing.T) {
	cfg, divergent, err := ParseQuotaConfig(map[string]interface{}{})
	if err != nil {
		t.Fatalf("ParseQuotaConfig: unexpected error: %v", err)
	}
	if !divergent {
		t.Fatal("ParseQuotaConfig: expected divergent=true for a missing [conductor.quota] section")
	}
	if len(cfg.SpillOrder) != 0 {
		t.Fatalf("ParseQuotaConfig: expected empty spill_order default, got %v", cfg.SpillOrder)
	}
}

func TestParseQuotaConfig_NilExtra_FailClosedDivergent(t *testing.T) {
	cfg, divergent, err := ParseQuotaConfig(nil)
	if err != nil {
		t.Fatalf("ParseQuotaConfig: unexpected error: %v", err)
	}
	if !divergent || len(cfg.SpillOrder) != 0 {
		t.Fatal("ParseQuotaConfig: nil extra must resolve to the fail-closed default")
	}
}

func TestParseQuotaConfig_Valid_ParsesSpillOrderAndCeilings(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{
				"spill_order":       []interface{}{"lane-a", "lane-b"},
				"ceiling":           map[string]interface{}{"lane-a": int64(100)},
				"personal_tracking": false,
			},
		},
	}
	cfg, divergent, err := ParseQuotaConfig(extra)
	if err != nil {
		t.Fatalf("ParseQuotaConfig: unexpected error: %v", err)
	}
	if divergent {
		t.Fatal("ParseQuotaConfig: a fully specified section must not report divergent")
	}
	if len(cfg.SpillOrder) != 2 || cfg.SpillOrder[0] != "lane-a" || cfg.SpillOrder[1] != "lane-b" {
		t.Fatalf("ParseQuotaConfig: spill_order = %v, want [lane-a lane-b]", cfg.SpillOrder)
	}
	if cfg.CeilingOverrides["lane-a"] != 100 {
		t.Fatalf("ParseQuotaConfig: ceiling[lane-a] = %d, want 100", cfg.CeilingOverrides["lane-a"])
	}
}

func TestParseQuotaConfig_InvalidSpillOrderType_Rejected(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{"spill_order": "not-an-array"},
		},
	}
	_, _, err := ParseQuotaConfig(extra)
	if err == nil {
		t.Fatal("ParseQuotaConfig: expected an error for a non-array spill_order")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseQuotaConfig: error kind = %v, want KindInvalidInput", err)
	}
}

func TestParseQuotaConfig_NegativeCeiling_Rejected(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{
				"ceiling": map[string]interface{}{"lane-a": int64(-1)},
			},
		},
	}
	_, _, err := ParseQuotaConfig(extra)
	if err == nil {
		t.Fatal("ParseQuotaConfig: expected an error for a negative ceiling")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseQuotaConfig: error kind = %v, want KindInvalidInput", err)
	}
}

func TestParseQuotaConfig_CeilingPlainIntType_Accepted(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{
				"ceiling": map[string]interface{}{"lane-a": int(50)},
			},
		},
	}
	cfg, _, err := ParseQuotaConfig(extra)
	if err != nil {
		t.Fatalf("ParseQuotaConfig: unexpected error: %v", err)
	}
	if cfg.CeilingOverrides["lane-a"] != 50 {
		t.Fatalf("ParseQuotaConfig: ceiling[lane-a] = %d, want 50", cfg.CeilingOverrides["lane-a"])
	}
}

func TestParseQuotaConfig_CeilingNonNumericType_Rejected(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{
				"ceiling": map[string]interface{}{"lane-a": "not-a-number"},
			},
		},
	}
	_, _, err := ParseQuotaConfig(extra)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseQuotaConfig: error kind = %v, want KindInvalidInput", err)
	}
}

func TestParseQuotaConfig_PersonalTrackingTrue_Rejected(t *testing.T) {
	extra := map[string]interface{}{
		"conductor": map[string]interface{}{
			"quota": map[string]interface{}{"personal_tracking": true},
		},
	}
	_, _, err := ParseQuotaConfig(extra)
	if err == nil {
		t.Fatal("ParseQuotaConfig: expected an error for personal_tracking=true (R-14.34 invariant)")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseQuotaConfig: error kind = %v, want KindInvalidInput", err)
	}
}

func TestQuotaPolicy_NextLane_AdvancesInSpillOrder(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b", "c"}}, clock)

	lane, err := p.NextLane(context.Background(), nil)
	if err != nil {
		t.Fatalf("NextLane: unexpected error: %v", err)
	}
	if lane != "a" {
		t.Fatalf("NextLane: got %q, want %q", lane, "a")
	}

	lane, err = p.NextLane(context.Background(), []LaneID{"a"})
	if err != nil {
		t.Fatalf("NextLane: unexpected error: %v", err)
	}
	if lane != "b" {
		t.Fatalf("NextLane: got %q, want %q", lane, "b")
	}
}

func TestQuotaPolicy_NextLane_SkipsRateLimitedLane(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b"}}, clock)
	p.markLimited("a")

	lane, err := p.NextLane(context.Background(), nil)
	if err != nil {
		t.Fatalf("NextLane: unexpected error: %v", err)
	}
	if lane != "b" {
		t.Fatalf("NextLane: got %q, want %q (a should still be within its rate-limit window)", lane, "b")
	}

	clock.Advance(defaultRateLimitWindow + time.Second)
	lane, err = p.NextLane(context.Background(), nil)
	if err != nil {
		t.Fatalf("NextLane: unexpected error: %v", err)
	}
	if lane != "a" {
		t.Fatalf("NextLane: got %q, want %q (a's window should have cleared)", lane, "a")
	}
}

func TestQuotaPolicy_NextLane_EmptySpillOrder_Exhausted(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{}, clock)

	_, err := p.NextLane(context.Background(), nil)
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("NextLane: error kind = %v, want KindQuotaExhausted (ErrAllLanesExhausted)", err)
	}
}

func TestQuotaPolicy_NextLane_AllExcluded_Exhausted(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b"}}, clock)

	_, err := p.NextLane(context.Background(), []LaneID{"a", "b"})
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("NextLane: error kind = %v, want KindQuotaExhausted", err)
	}
}

func TestQuotaPolicy_NextLane_ContextCanceled(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a"}}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.NextLane(ctx, nil)
	if err == nil {
		t.Fatal("NextLane: expected an error for a canceled context")
	}
}

// TestNextLane_SoleCallerIsRouter is the arch test R-21.264's acceptance
// criterion requires: NextLane is an ordering function, not a selector,
// and P1-E11-W3-S22-T2's Router is the only permitted caller. That Router
// has not landed as of this ticket (internal/conductor/router.go does not
// exist yet -- see internal/build/testonly-allow.json's
// conductor.QuotaPolicy.NextLane entry), so today the only occurrences of
// ".NextLane(" in the whole tree must be this package's own definition,
// its own Advance call (spill.go), and this file's tests. If a future
// change adds a call from anywhere else without updating this test, that
// is exactly the "no other call site" violation R-21.264 forbids.
func TestNextLane_SoleCallerIsRouter(t *testing.T) {
	repoRoot := findRepoRoot(t)
	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if filepath.Dir(rel) == filepath.Join("internal", "conductor") {
			return nil // this package's own definition/callers/tests
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), ".NextLane(") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo for NextLane call sites: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("NextLane called outside internal/conductor (expected sole caller: P1-E11-W3-S22-T2's Router): %v", offenders)
	}
}

// findRepoRoot walks up from the working directory to the nearest
// directory containing go.mod, so the arch test works under `go test
// ./internal/conductor/...` regardless of the invocation's cwd.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("findRepoRoot: no go.mod found walking up from the test's working directory")
		}
		dir = parent
	}
}
