// Package build (this file) holds ledgeridentitygate.go/_coverage.go's
// seeded-violation RED proof, its false-positive GREEN proofs, and the
// real-tree green check, matching schemaceilinggate_test.go's own
// (now-retired) split convention for one gate's test file.
package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const ledgerIdentityFixtureDir = "internal/build/testdata/seeded-violations/ledgeridentity"

func ledgerIdentityModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("ledger identity gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("ledger identity gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestLedgerIdentityGate_MissingSetIDRed proves the gate catches a
// migrate.MigrationSet{...} literal with no SetID key at all.
func TestLedgerIdentityGate_MissingSetIDRed(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	files := []string{filepath.Join(ledgerIdentityFixtureDir, "missing_id", "caller.go")}
	v, err := CheckLedgerIdentity(root, files)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("ledger identity gate: want exactly 1 violation, got %d: %+v", len(v), v)
	}
	if v[0].Kind != "missing" {
		t.Fatalf("ledger identity gate: want Kind=missing, got %+v", v[0])
	}
}

// TestLedgerIdentityGate_DuplicateSetIDRed proves the gate catches two
// literals in DIFFERENT packages claiming the same literal SetID value.
func TestLedgerIdentityGate_DuplicateSetIDRed(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	files := []string{
		filepath.Join(ledgerIdentityFixtureDir, "dup_a", "caller_a.go"),
		filepath.Join(ledgerIdentityFixtureDir, "dup_b", "caller_b.go"),
	}
	v, err := CheckLedgerIdentity(root, files)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("ledger identity gate: want exactly 1 violation, got %d: %+v", len(v), v)
	}
	if v[0].Kind != "duplicate" || v[0].SetID != "dup-slot" {
		t.Fatalf("ledger identity gate: want a dup-slot duplicate, got %+v", v[0])
	}
}

// TestLedgerIdentityGate_CleanNotFlagged is the false-positive proof for
// a properly-identified, tree-uniquely-named SetID.
func TestLedgerIdentityGate_CleanNotFlagged(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	files := []string{filepath.Join(ledgerIdentityFixtureDir, "clean_unique", "caller.go")}
	v, err := CheckLedgerIdentity(root, files)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("ledger identity gate: want zero violations, got %+v", v)
	}
}

// TestLedgerIdentityGate_ComputedSetIDNotFlagged is the false-positive
// proof matching internal/storage/plugin_migrate.go's pluginSetID
// shape: a SetID key IS present but its value depends on a runtime
// argument (a "+"-concatenation with a non-literal operand, or a
// function call) rather than being a plain string literal. The gate must
// treat presence as satisfied and never attempt (or fail attempting) a
// duplicate check against an unresolvable value.
func TestLedgerIdentityGate_ComputedSetIDNotFlagged(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	files := []string{filepath.Join(ledgerIdentityFixtureDir, "computed_ok", "caller.go")}
	v, err := CheckLedgerIdentity(root, files)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("ledger identity gate: want zero violations, got %+v", v)
	}
}

// TestLedgerIdentityGate_RealTreeGreen runs the real derivation against
// every git-tracked file, proving the CURRENT tree's production
// MigrationSet literals all carry a SetID and none collide.
func TestLedgerIdentityGate_RealTreeGreen(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	files, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	v, err := CheckLedgerIdentityTracked(root, files)
	if err != nil {
		t.Fatalf("ledger identity gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("ledger identity gate: real tree has %d ledger-identity violation(s):\n%v", len(v), v)
	}
}

// TestLedgerIdentitySkipsPathSilencesOnlyFixtures proves the real-tree
// filter is doing exactly one job. A path filter that quietly swallowed
// genuine violations would be strictly worse than the red it was added to
// fix, so this asserts BOTH halves against the SAME input: the raw
// CheckLedgerIdentity still sees the two seeded violations, and only the
// Tracked wrapper drops them. If a future edit widens the filter, the
// first assertion keeps holding while the second silently starts hiding
// real findings — so the two are checked together, never apart.
func TestLedgerIdentitySkipsPathSilencesOnlyFixtures(t *testing.T) {
	root := ledgerIdentityModuleRoot(t)
	fixtures := []string{
		filepath.Join(ledgerIdentityFixtureDir, "missing_id", "caller.go"),
		filepath.Join(ledgerIdentityFixtureDir, "dup_a", "caller_a.go"),
		filepath.Join(ledgerIdentityFixtureDir, "dup_b", "caller_b.go"),
	}

	raw, err := CheckLedgerIdentity(root, fixtures)
	if err != nil {
		t.Fatalf("unfiltered scan: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("unfiltered scan found no violations in the seeded fixtures: the fixtures " +
			"themselves are broken, so the real-tree filter below proves nothing")
	}

	filtered, err := CheckLedgerIdentityTracked(root, fixtures)
	if err != nil {
		t.Fatalf("filtered scan: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered scan kept %d fixture violation(s), want 0: %v", len(filtered), filtered)
	}

	if LedgerIdentitySkipsPath("internal/storage/migrate/ledger.go") {
		t.Error("filter skips a real source path; it would hide genuine violations")
	}
	if !LedgerIdentitySkipsPath(fixtures[0]) {
		t.Errorf("filter does not skip fixture path %q", fixtures[0])
	}
}
