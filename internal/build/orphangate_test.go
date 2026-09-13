package build

// Purpose: the real-tree assertions for DEFECT-orphaned-allow-entries-
// systemic.md's gates: the checked-in allow list, the tracked
// closed-ticket manifest, and the grandfather list must be mutually
// consistent right now, and the tracked debt count must never rise
// silently. Mutation proofs for the individual predicates (built against
// synthetic fixtures, never the real allow list) live in
// orphangate_seeded_test.go; this file only exercises the real data,
// matching the split testonlygate_test.go / testonlygate_validation_test.go
// already use for the sibling gate.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	closedTicketManifestPath  = "testdata/closed-ticket-manifest.json"
	orphanGrandfatherListPath = "testdata/testonly-orphan-grandfather.txt"
	testonlyDebtBaselinePath  = "testdata/testonly-debt-baseline.txt"
)

// loadRealAllowAndClosed loads the two real, tracked inputs every gate in
// this file needs: the allow list and the closed-ticket manifest.
func loadRealAllowAndClosed(t *testing.T) (root string, allow map[string]TestOnlyAllowEntry, closed map[string]bool) {
	t.Helper()
	root = moduleRoot(t)
	var err error
	allow, err = LoadTestOnlyAllowList(filepath.Join(root, "internal", "build", "testonly-allow.json"))
	if err != nil {
		t.Fatalf("loading testonly-allow.json: %v", err)
	}
	closed, err = LoadClosedTicketManifest(filepath.Join(root, "internal", "build", closedTicketManifestPath))
	if err != nil {
		t.Fatalf("loading closed-ticket manifest: %v", err)
	}
	return root, allow, closed
}

// readOrphanGrandfatherList reads the shrink-only list of symbols exempt
// from TestNoFreshOrphanedAllowEntries because they predate the gate.
func readOrphanGrandfatherList(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "internal", "build", orphanGrandfatherListPath))
	if err != nil {
		t.Fatalf("reading orphan grandfather list: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		// "#" introduces a comment so the list can carry the header
		// explaining what it is and that it may only ever shrink. An
		// unexplained exemption list is how the debt this gate exists to
		// stop became invisible in the first place.
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out[s] = true
	}
	return out
}

// TestNoFreshOrphanedAllowEntries is DEFECT-orphaned-allow-entries-
// systemic.md's gate 2, THE LOAD-BEARING ONE: an allow entry whose
// retire_ticket is already closed and whose caller_site was never
// created is refused, unless it is already on the shrink-only
// grandfather list frozen when this gate landed (61 entries, matching
// the DEFECT journal's own final tally). A NEW orphan (not on that list)
// fails outright: this removes the hiding place the UNOWNED cap created.
func TestNoFreshOrphanedAllowEntries(t *testing.T) {
	root, allow, closed := loadRealAllowAndClosed(t)
	grandfather := readOrphanGrandfatherList(t, root)

	orphaned := FindOrphanedAllowEntries(root, allow, closed)
	var fresh []OrphanedAllowEntry
	for _, o := range orphaned {
		if !grandfather[o.Symbol] {
			fresh = append(fresh, o)
		}
	}
	if len(fresh) > 0 {
		var b strings.Builder
		for _, o := range fresh {
			b.WriteString("\n  " + o.Symbol + " -> retire_ticket " + o.RetireTicket + " is closed, caller_site " +
				o.CallerSite + " does not exist")
		}
		t.Fatalf("test-only gate: %d allow entr(y/ies) name a retire_ticket that is ALREADY DONE and a "+
			"caller_site that was never created:%s\n\nThis is exactly the hiding place DEFECT-orphaned-allow-"+
			"entries-systemic.md diagnosed. Either wire the symbol, correct caller_site, or record the gap "+
			"honestly by adding it to %s (reviewed, shrink-only).", len(fresh), b.String(), orphanGrandfatherListPath)
	}
	if len(orphaned) < len(grandfather) {
		t.Logf("test-only gate: orphaned entries are down to %d from a grandfathered %d. Shrink %s to match.",
			len(orphaned), len(grandfather), orphanGrandfatherListPath)
	}
}

// TestNoSelfReferentialAllowEntries is gate 1: an entry whose
// AddedByTicket names the same ticket as its own RetireTicket is a
// closed loop by construction and is refused outright, with no
// grandfather exception (the field is new and forward-only, so no
// legacy entry can trip it).
func TestNoSelfReferentialAllowEntries(t *testing.T) {
	_, allow, _ := loadRealAllowAndClosed(t)
	if bad := FindSelfReferentialAllowEntries(allow); len(bad) > 0 {
		t.Fatalf("test-only gate: %d allow entr(y/ies) name their own authoring ticket as retire_ticket, a "+
			"closed loop by construction: %v", len(bad), bad)
	}
}

// TestTrackedDeadSurfaceCountCannotGrow is gate 4: UNOWNED and orphaned
// entries are counted together, so an honest UNOWNED recording is never
// penalised relative to a hidden one. The count may fall freely; it may
// not rise past the baseline captured when this gate landed (103: 42
// UNOWNED + 61 orphaned).
func TestTrackedDeadSurfaceCountCannotGrow(t *testing.T) {
	root, allow, closed := loadRealAllowAndClosed(t)
	orphaned := FindOrphanedAllowEntries(root, allow, closed)
	got := TrackedDeadSurfaceCount(allow, orphaned)

	raw, err := os.ReadFile(filepath.Join(root, "internal", "build", testonlyDebtBaselinePath))
	if err != nil {
		t.Fatalf("reading debt baseline: %v", err)
	}
	baseline, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s must hold a single integer: %v", testonlyDebtBaselinePath, err)
	}
	if got > baseline {
		t.Fatalf("test-only gate: tracked dead-surface count (UNOWNED + orphaned) is %d, above the baseline of "+
			"%d. A new gap must be wired, marked UNOWNED, or grandfathered; it may not raise this number "+
			"silently.", got, baseline)
	}
	if got < baseline {
		t.Logf("test-only gate: tracked dead-surface count is down to %d from a baseline of %d. Lower %s to %d.",
			got, baseline, testonlyDebtBaselinePath, got)
	}
}
