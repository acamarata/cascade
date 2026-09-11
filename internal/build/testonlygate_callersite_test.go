package build

// Purpose: the two real-tree assertions that make a test-only exemption
// falsifiable rather than decorative (R-14.223). Without these, the
// caller_site field and the UNOWNED count are data nothing reads, and the
// gate goes back to measuring only that someone typed a sentence.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unownedBaselinePath holds the number of allow-list entries that name no
// owning ticket. It is a checked-in file rather than a literal in this test
// so that lowering it is a visible, reviewable change to tracked data.
const unownedBaselinePath = "testdata/testonly-unowned-baseline.txt"

// TestCallerSitesAreWiredOnceTheirFileExists is the falsifiability check.
// An exemption names the file that will eventually call the symbol. While
// that file does not exist, the promise is about genuinely future work and
// is left alone. The moment it exists, it must already reference the
// symbol, or the promise has been broken: the owning ticket landed its
// file and forgot the wiring, which is exactly the failure the old
// free-text expected_caller could never catch.
func TestCallerSitesAreWiredOnceTheirFileExists(t *testing.T) {
	root := deadcodeModuleRoot(t)
	allow, err := LoadTestOnlyAllowList(filepath.Join(root, "internal", "build", "testonly-allow.json"))
	if err != nil {
		t.Fatal(err) // fail closed: an unreadable allow list blocks, never skips
	}

	broken, err := CheckCallerSitesWired(root, deadcodeModulePath, allow)
	if err != nil {
		t.Fatalf("test-only gate: checking caller sites: %v", err)
	}

	// The legacy baseline. When caller_site was retrofitted onto the
	// entries that predate it, the value was derived from each entry's
	// existing prose, and for a large minority that prose named a file
	// that exists but was never going to hold the call. Those are frozen
	// here, by the same shrink-only rule R-14.223 applies to the UNOWNED
	// count, so that bad legacy data stays VISIBLE without blocking every
	// unrelated commit, while a NEW broken promise still fails outright.
	baseline := readCallerSiteBaseline(t, root)
	var fresh []BrokenCallerSite
	for _, e := range broken {
		if !baseline[e.Symbol] {
			fresh = append(fresh, e)
		}
	}

	if len(fresh) > 0 {
		var b strings.Builder
		for _, e := range fresh {
			b.WriteString("\n  " + e.Symbol + " -> " + e.CallerSite)
		}
		t.Fatalf("test-only gate: %d exemption(s) name a caller_site that now EXISTS but does not "+
			"reference the symbol:%s\n\nThe file the exemption promised would call this symbol has "+
			"landed without the call. Either wire it, or correct caller_site to the file that will "+
			"really make the call. A promise that cannot come true is worse than no exemption.",
			len(fresh), b.String())
	}

	if len(broken) < len(baseline) {
		t.Logf("test-only gate: the legacy broken-caller_site set is down to %d from a baseline of %d. "+
			"Regenerate %s so the ratchet holds at the new level.",
			len(broken), len(baseline), callerSiteBaselinePath)
	}
}

// callerSiteBaselinePath lists the symbols whose retrofitted caller_site
// already pointed at an existing file that does not call them. Checked in
// rather than computed so that growing it is a visible change to tracked
// data, and so the set can only shrink.
const callerSiteBaselinePath = "testdata/testonly-callersite-baseline.txt"

func readCallerSiteBaseline(t *testing.T, root string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "internal", "build", callerSiteBaselinePath))
	if err != nil {
		t.Fatalf("test-only gate: reading the caller_site baseline: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out[s] = true
		}
	}
	return out
}

// TestUnownedExemptionCountCannotGrow freezes the legacy rot. Entries whose
// text named no ticket were migrated to the UNOWNED escape hatch rather
// than invented owners, which is honest but would let the list keep growing
// under a stricter-looking rule while nothing ever retires. The count may
// fall freely; it may not rise.
func TestUnownedExemptionCountCannotGrow(t *testing.T) {
	root := deadcodeModuleRoot(t)
	allow, err := LoadTestOnlyAllowList(filepath.Join(root, "internal", "build", "testonly-allow.json"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "internal", "build", unownedBaselinePath))
	if err != nil {
		t.Fatalf("test-only gate: reading the UNOWNED baseline: %v", err)
	}
	baseline, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("test-only gate: %s must hold a single integer: %v", unownedBaselinePath, err)
	}

	got := UnownedTicketCount(allow)
	if got > baseline {
		t.Fatalf("test-only gate: %d exemption(s) now carry retire_ticket %q, above the baseline of %d.\n\n"+
			"A new exemption must name the ticket that will retire it. %q exists only to record the "+
			"legacy entries whose text named no owner, and that set is not allowed to grow.",
			got, UnownedTicket, baseline, UnownedTicket)
	}
	if got < baseline {
		t.Logf("test-only gate: UNOWNED exemptions are down to %d from a baseline of %d. "+
			"Lower %s to %d so the ratchet holds at the new level.", got, baseline, unownedBaselinePath, got)
	}
}
