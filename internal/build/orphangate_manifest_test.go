package build

// Purpose: the staleness half of the CI-visibility solution
// (orphangate.go's doc comment). Where a real
// .claude/planning/p1/phase/journals directory exists (any developer's
// own checkout), TestClosedTicketManifestIsCurrent re-scans it with the
// exact filename rule internal/build/gen/closedtickets uses and fails if
// the tracked manifest disagrees. Where it does not (every CI checkout,
// since .claude/ is gitignored), it calls t.Skip with an explicit,
// printed reason instead of passing in silence: an unconditionally green
// test that never actually looked at anything is the exact failure mode
// DEFECT-orphaned-allow-entries-systemic.md diagnosed, and this file must
// not reproduce it.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// manifestCurrencyTicketPattern mirrors
// internal/build/gen/closedtickets's journalTicketPattern exactly. It is
// redeclared rather than shared because internal/build cannot import a
// main package, and duplicating a one-line regex is cheaper and clearer
// than restructuring either side around a shared library just for it.
var manifestCurrencyTicketPattern = regexp.MustCompile(`^(P1-E\d{1,3}-W\d{1,2}-S\d{1,3}-T\d{1,3})\.md$`)

// scanLiveClosedTickets re-derives the closed-ticket set directly from
// disk, independently of anything already loaded from the tracked
// manifest.
func scanLiveClosedTickets(t *testing.T, journalsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(journalsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", journalsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := manifestCurrencyTicketPattern.FindStringSubmatch(e.Name()); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// diffTicketSets returns ids in live but not tracked, and ids in tracked
// but not live.
func diffTicketSets(live []string, tracked map[string]bool) (missing, extra []string) {
	liveSet := make(map[string]bool, len(live))
	for _, id := range live {
		liveSet[id] = true
		if !tracked[id] {
			missing = append(missing, id)
		}
	}
	for id := range tracked {
		if !liveSet[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return missing, extra
}

func TestClosedTicketManifestIsCurrent(t *testing.T) {
	root := moduleRoot(t)
	journalsDir := filepath.Join(root, ".claude", "planning", "p1", "phase", "journals")

	if _, err := os.Stat(journalsDir); err != nil {
		t.Skipf("closed-ticket manifest staleness check SKIPPED: %s does not exist in this checkout "+
			"(expected in CI and in any clone that never ran the phase locally -- .claude/ is gitignored). "+
			"CI relies on the tracked snapshot at %s instead. Run "+
			"`go run ./internal/build/gen/closedtickets` locally before closing a ticket to keep it current.",
			journalsDir, closedTicketManifestPath)
	}

	live := scanLiveClosedTickets(t, journalsDir)
	tracked, err := LoadClosedTicketManifest(filepath.Join(root, "internal", "build", closedTicketManifestPath))
	if err != nil {
		t.Fatalf("loading tracked closed-ticket manifest: %v", err)
	}

	missing, extra := diffTicketSets(live, tracked)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("closed-ticket manifest %s is STALE against the real journals directory.\n"+
			"closed since the last regeneration, missing from the manifest: %v\n"+
			"in the manifest but no longer closed, or never was: %v\n\n"+
			"Run `go run ./internal/build/gen/closedtickets` and commit the result.",
			closedTicketManifestPath, missing, extra)
	}
}
