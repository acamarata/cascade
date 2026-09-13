// Package build (this file) implements the orphaned-allow-entry gates
// filed by .claude/planning/p1/phase/journals/
// DEFECT-orphaned-allow-entries-systemic.md (T0, 2026-09-13): the
// mechanisms that make a closed-loop test-only exemption impossible to
// hide again.
//
// The defect measured 227 testonly-allow.json entries: 42 UNOWNED
// (visible, tracked, shrink-only) and 61 more that name a retire_ticket
// which is ALREADY DONE and a caller_site that was never created. Those
// 61 are invisible to every gate that predates this file: the entry is
// not UNOWNED, so the shrink-only ratchet never counts it, and its
// retire ticket is closed, so nothing will ever revisit it. Real
// permanently-unowned dead surface was 103 entries, not the 42 tracked.
//
// ROOT CAUSE, already diagnosed and not re-litigated here: the UNOWNED
// ratchet is capped and shrink-only, so a lane with a genuine new gap
// cannot record it there. Naming some OTHER ticket as retire_ticket is
// unconstrained and unchecked, so that is where the debt goes, and it
// becomes invisible the moment that ticket closes. Tightening the
// UNOWNED cap further would only increase the pressure that caused the
// relocation; these gates remove the hiding place instead.
//
// THE CI-VISIBILITY CONSTRAINT. "Is ticket X done?" is answered today by
// the existence of .claude/planning/p1/phase/journals/<ticket>.md, and
// .claude/ is gitignored: a CI checkout never has it. These gates read a
// TRACKED snapshot instead, testdata/closed-ticket-manifest.json,
// regenerated on a developer's own machine (where the journals DO exist,
// the same precedent internal/inventory/gen already sets for
// counts.json and registry.json) by internal/build/gen/closedtickets,
// then committed like any other generated, tracked artifact.
//
// A checkout that finds the tracked snapshot missing FAILS LOUD:
// LoadClosedTicketManifest returns an error, the same fail-closed shape
// LoadTestOnlyAllowList already uses for a missing allow list read as a
// hard error rather than "nothing to check". The complementary staleness
// check, TestClosedTicketManifestIsCurrent
// (orphangate_manifest_test.go), re-scans the journals directory and
// fails if the tracked manifest disagrees with it WHERE THAT DIRECTORY
// EXISTS; where it does not (every CI checkout), it calls t.Skip with an
// explicit, printed reason instead of passing in silence. Silence is the
// failure mode DEFECT-orphaned-allow-entries-systemic.md diagnosed; nothing
// in this file is allowed to reproduce it.
package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// closedTicketManifestFile is the on-disk shape of
// testdata/closed-ticket-manifest.json. GeneratedAt is informational only
// (when the snapshot was last regenerated); LoadClosedTicketManifest does
// not validate or compare it, only Tickets.
type closedTicketManifestFile struct {
	GeneratedAt string   `json:"generated_at"`
	Tickets     []string `json:"tickets"`
}

// LoadClosedTicketManifest reads the tracked closed-ticket snapshot at
// path and returns the set of ticket ids it names. A missing, empty, or
// malformed file is an error, never an empty set: this gate's entire
// point is that "I could not see the data" must never read the same as
// "there is nothing to flag". A genuinely empty manifest (zero closed
// tickets) is also refused, because this tree has never actually been in
// that state and a manifest that says so is far more likely corrupt or
// truncated than accurate.
func LoadClosedTicketManifest(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("closed-ticket manifest: reading %s: %w (a CI checkout never has "+
			".claude/planning/p1/phase/journals; every gate that needs ticket-closure state relies "+
			"on this tracked snapshot, so a missing file must fail the gate loudly, not skip it)",
			path, err)
	}
	var file closedTicketManifestFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("closed-ticket manifest: parsing %s: %w", path, err)
	}
	if len(file.Tickets) == 0 {
		return nil, fmt.Errorf("closed-ticket manifest: %s names zero closed tickets; this tree has "+
			"closed hundreds, so treat an empty list as a corrupt or truncated manifest, not a first run",
			path)
	}
	out := make(map[string]bool, len(file.Tickets))
	for _, t := range file.Tickets {
		out[t] = true
	}
	return out, nil
}

// OrphanedAllowEntry is one allow-list entry whose retire_ticket is
// already closed and whose caller_site was never created: exactly the
// escape route DEFECT-orphaned-allow-entries-systemic.md diagnosed. It is
// also what a caller_site check inverse of
// TestCallerSitesAreWiredOnceTheirFileExists would flag (that DEFECT's
// gate 5): "closed ticket, missing file" is one predicate, computed once
// here and consumed both by the orphan-refusal gate and by the tracked
// debt count, rather than reimplemented twice.
type OrphanedAllowEntry struct {
	Symbol       string
	RetireTicket string
	CallerSite   string
}

// FindOrphanedAllowEntries returns every allow entry whose retire_ticket
// is in closed and whose caller_site file does not exist under
// moduleRoot, sorted by symbol. UNOWNED entries are never orphaned: they
// name no ticket to close. An entry whose caller_site file DOES exist is
// not orphaned by this definition even if CheckCallerSitesWired later
// finds it does not actually reference the symbol; that is a different,
// already-caught failure (a broken promise on a file that landed), not
// an invisible one on a file that never will.
func FindOrphanedAllowEntries(
	moduleRoot string, allow map[string]TestOnlyAllowEntry, closed map[string]bool,
) []OrphanedAllowEntry {
	var out []OrphanedAllowEntry
	for key, e := range allow {
		if e.RetireTicket == UnownedTicket {
			continue
		}
		if !closed[e.RetireTicket] {
			continue
		}
		if _, err := os.Stat(filepath.Join(moduleRoot, e.CallerSite)); err == nil {
			continue
		}
		out = append(out, OrphanedAllowEntry{Symbol: key, RetireTicket: e.RetireTicket, CallerSite: e.CallerSite})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// FindSelfReferentialAllowEntries returns every allow entry whose
// AddedByTicket names the same ticket as its own RetireTicket: a closed
// loop by construction, since the ticket meant to retire the entry is
// the one that just created it. Entries with no recorded AddedByTicket
// (every legacy entry, and any new one whose lane did not populate the
// optional field) are skipped rather than guessed at. This is defense in
// depth, not the only backstop: the DONE-retire_ticket check above, plus
// the closedtickets generator's closure-time refusal (gate 3), still
// catch a self-referential entry once its own ticket actually closes,
// which is the only moment it can do real harm.
func FindSelfReferentialAllowEntries(allow map[string]TestOnlyAllowEntry) []string {
	var out []string
	for key, e := range allow {
		if e.AddedByTicket == "" {
			continue
		}
		if e.AddedByTicket == e.RetireTicket {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TrackedDeadSurfaceCount is the single number
// DEFECT-orphaned-allow-entries-systemic.md asked for: UNOWNED entries
// plus orphaned ones, counted together so an honest lane that could not
// find a caller (and left the gap findable, as UNOWNED) is never worse
// off in the tracked total than one that named a since-closed ticket and
// vanished from every other count.
func TrackedDeadSurfaceCount(allow map[string]TestOnlyAllowEntry, orphaned []OrphanedAllowEntry) int {
	return UnownedTicketCount(allow) + len(orphaned)
}
