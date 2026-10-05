// Package coverage (this file): BuildCoverageMatrix/GapReport tests,
// including the seeded-violation RED proof, and the guarded live-tree
// gate (TestInventoryCoverage_RealTree). Split from covmatrix_test.go to
// stay under the 300-line file cap.
package coverage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- BuildCoverageMatrix / GapReport: the seeded-violation proof ---------

// TestCoverageMatrix_SeededGap is the RED proof: an inventory row with no
// citation, and one whose citation names neither a real ticket nor a real
// deferral, are BOTH reported as gaps; a row with a resolvable citation
// is not.
func TestCoverageMatrix_SeededGap(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "phase", "epics",
		"E-K", "waves", "W-3", "sprints", "S-23", "tickets", "T-6.yaml"), "id: P1-E11-W3-S23-T6\n")

	rows := []InventoryRow{
		{Section: "S", Index: 1, Text: "| covered by ticket | CORE (K/S-23.T6) |"},
		{Section: "S", Index: 2, Text: "| covered by deferral | DEF-P2-example |"},
		{Section: "S", Index: 3, Text: "| no citation at all | CORE |"},
		{Section: "S", Index: 4, Text: "| citation to nothing real | CORE (K/S-99.T9) |"},
	}
	tickets := []TicketRecord{{ID: "P1-E11-W3-S23-T6", Path: "irrelevant"}}
	deferrals := []DeferralEntry{{ID: "DEF-P2-example"}}

	coverage, err := BuildCoverageMatrix(root, rows, tickets, deferrals)
	if err != nil {
		t.Fatalf("BuildCoverageMatrix: %v", err)
	}
	report := GapReport(coverage)
	if report.Empty() {
		t.Fatal("expected two seeded gaps, got zero")
	}
	if len(report.Gaps) != 2 {
		t.Fatalf("got %d gaps, want 2: %s", len(report.Gaps), report)
	}
	names := map[string]bool{}
	for _, g := range report.Gaps {
		names[g.RowID()] = true
	}
	if !names["S #3"] || !names["S #4"] {
		t.Fatalf("gap set = %v, want rows #3 and #4", names)
	}
	if strings.Contains(report.String(), "#1") || strings.Contains(report.String(), "#2") {
		t.Fatalf("covered rows leaked into the gap report: %s", report)
	}
}

func TestCoverageMatrix_CleanIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "phase", "epics",
		"E-K", "waves", "W-3", "sprints", "S-23", "tickets", "T-6.yaml"), "id: P1-E11-W3-S23-T6\n")
	rows := []InventoryRow{{Section: "S", Index: 1, Text: "| x | CORE (K/S-23.T6) |"}}
	tickets := []TicketRecord{{ID: "P1-E11-W3-S23-T6", Path: "irrelevant"}}
	coverage, err := BuildCoverageMatrix(root, rows, tickets, nil)
	if err != nil {
		t.Fatalf("BuildCoverageMatrix: %v", err)
	}
	if report := GapReport(coverage); !report.Empty() {
		t.Fatalf("expected zero gaps, got %s", report)
	}
}

func TestBuildCoverageMatrix_ZeroTicketsFailsClosed(t *testing.T) {
	if _, err := BuildCoverageMatrix(t.TempDir(), []InventoryRow{{Section: "S", Index: 1, Text: "x"}}, nil, nil); err == nil {
		t.Fatal("expected an error when tickets is empty")
	}
}

// --- Live tree ------------------------------------------------------------

// planningTreeEnv names the environment variable that points the live check
// at a planning tree: the directory holding phase/ and the feature
// inventory document. Nothing here knows where that tree lives.
const planningTreeEnv = "CASCADE_PLANNING_TREE"

// TestInventoryCoverage_RealTree is a local-only diagnostic. It runs only
// when CASCADE_PLANNING_TREE names a planning tree; unset (public CI, a
// clone without one), it skips with a printed reason. A skip is not
// evidence: no acceptance or gate relies on this test, and the fixture
// tests above (t.TempDir roots) carry the coverage. When it runs, it
// asserts the inventory itself is non-empty (LoadInventory's own
// fail-closed law already refuses an empty file, restated here as an
// explicit assertion so a future refactor cannot silently drop it) and
// reports every unresolved row without hiding the count.
func TestInventoryCoverage_RealTree(t *testing.T) {
	tree := os.Getenv(planningTreeEnv)
	if tree == "" {
		t.Skip(planningTreeEnv + " is unset: local-only inventory-coverage diagnostic not run (not evidence)")
	}
	root := findModuleRoot(t)
	phaseDir := filepath.Join(tree, "phase")
	if _, err := os.Stat(phaseDir); err != nil {
		t.Fatalf("%s=%q has no phase directory: %v", planningTreeEnv, tree, err)
	}
	rows, err := LoadInventory(filepath.Join(tree, "01-FEATURE-INVENTORY.md"))
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("live inventory produced zero rows")
	}
	tickets, err := LoadTicketTree(filepath.Join(phaseDir, "epics"))
	if err != nil {
		t.Fatalf("LoadTicketTree: %v", err)
	}
	deferrals, err := LoadDeferrals(filepath.Join(phaseDir, "deferrals.yaml"))
	if err != nil {
		t.Fatalf("LoadDeferrals: %v", err)
	}
	coverage, err := BuildCoverageMatrix(tree, rows, tickets, deferrals)
	if err != nil {
		t.Fatalf("BuildCoverageMatrix: %v", err)
	}
	report := GapReport(coverage)
	t.Logf("inventory coverage: %d row(s), %d ticket(s), %d deferral(s), %d gap(s)",
		len(rows), len(tickets), len(deferrals), len(report.Gaps))

	// SHRINK-ONLY BASELINE, not an exemption. The lose-nothing rule says P1
	// is not releasable until every inventory row is covered by a ticket or
	// an explicit deferral, and today 62 rows are covered by NEITHER: their
	// disposition column names a category ("CORE", "CORE + T1") instead of
	// citing a ticket, so nothing mechanical can tell a covered row from a
	// forgotten one. That is a real, pre-existing property of the source
	// document and it is exactly what this gate exists to surface.
	//
	// It is baselined rather than hard-failed because it was true long
	// before this gate existed, and a gate that is red on arrival gets
	// disabled rather than fixed. The number may fall freely; it may not
	// rise. Art.8 still requires it to reach ZERO before release, and
	// nothing here weakens that: see R-14.228.
	baseline := readGapBaseline(t, root)
	if len(report.Gaps) > baseline {
		t.Fatalf("inventory coverage gap COUNT ROSE to %d from a baseline of %d. A new row now resolves "+
			"to neither a ticket citation nor a deferral:\n%s", len(report.Gaps), baseline, report)
	}
	if len(report.Gaps) < baseline {
		t.Logf("inventory coverage gaps are down to %d from a baseline of %d. Lower %s to %d "+
			"so the ratchet holds at the new level.", len(report.Gaps), baseline, gapBaselinePath, len(report.Gaps))
	}
}

// gapBaselinePath records how many inventory rows currently resolve to
// neither a ticket nor a deferral. Checked in so that raising it is a
// visible change to tracked data rather than an edit to a test literal.
const gapBaselinePath = "internal/coverage/testdata/gap-baseline.txt"

func readGapBaseline(t *testing.T, root string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, gapBaselinePath))
	if err != nil {
		t.Fatalf("reading the inventory-gap baseline: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s must hold a single integer: %v", gapBaselinePath, err)
	}
	return n
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from the test's working directory")
		}
		dir = parent
	}
}
