// Package build (this file) implements the orphaned-allow-entry gates: the
// mechanisms that make a closed-loop test-only exemption impossible to hide.
//
// An allow entry that names a retire ticket which is ALREADY DONE and a
// caller_site that was never created is invisible to every older gate: it is
// not UNOWNED, so the shrink-only ratchet never counts it, and its ticket is
// closed, so nothing revisits it. The UNOWNED cap pushed new gaps there;
// these gates remove the hiding place instead of tightening the cap.
//
// Closure state comes from ONE tracked source, testdata/closed-ticket-
// manifest.json, exported from the plan and committed by the ship tool. Its
// `tickets` list is the closed set and its `known` list is every live plan
// ticket id (LoadKnownTicketIDs). A CI checkout reads the same file a
// developer does; a missing, malformed or empty manifest FAILS LOUD rather
// than reading as "nothing to flag". Capability-keyed rows (retire key
// "cap:<id>") stand for tickets that closed before the plan re-key, so the
// orphan predicate treats them as closed; testdata/capability-ids.txt, also
// ship-owned, lists the capability ids a row may name (LoadCapabilityIDs).
package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// closedTicketManifestFile is the on-disk shape of
// testdata/closed-ticket-manifest.json. GeneratedAt is informational only.
type closedTicketManifestFile struct {
	GeneratedAt string   `json:"generated_at"`
	Tickets     []string `json:"tickets"`
	Known       []string `json:"known"`
}

// readClosedTicketManifest reads and parses the manifest at path. A missing
// or malformed file is an error.
func readClosedTicketManifest(path string) (closedTicketManifestFile, error) {
	var file closedTicketManifestFile
	data, err := os.ReadFile(path)
	if err != nil {
		return file, fmt.Errorf("closed-ticket manifest: reading %s: %w (the tracked manifest is the only "+
			"closure source for these gates, so a missing file fails the gate loudly, never skips it)", path, err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("closed-ticket manifest: parsing %s: %w", path, err)
	}
	return file, nil
}

// idSet turns a list of ids into a set.
func idSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// LoadClosedTicketManifest reads the tracked closed-ticket snapshot at
// path and returns the set of ticket ids it names. A missing, empty, or
// malformed file is an error, never an empty set: "I could not see the
// data" must never read the same as "there is nothing to flag". A manifest
// with zero closed tickets is refused as corrupt or truncated.
func LoadClosedTicketManifest(path string) (map[string]bool, error) {
	file, err := readClosedTicketManifest(path)
	if err != nil {
		return nil, err
	}
	if len(file.Tickets) == 0 {
		return nil, fmt.Errorf("closed-ticket manifest: %s names zero closed tickets; treat an empty list as a "+
			"corrupt or truncated manifest, not a first run", path)
	}
	return idSet(file.Tickets), nil
}

// LoadKnownTicketIDs reads the manifest's `known` list: every live plan
// ticket id a retire_ticket, expires or added_by_ticket may name. It has
// LoadClosedTicketManifest's fail-loud rules: a missing or malformed file, or
// an absent or empty list, is an error. A closed ticket missing from `known`
// is refused too, since the two lists come from one export.
func LoadKnownTicketIDs(path string) (map[string]bool, error) {
	file, err := readClosedTicketManifest(path)
	if err != nil {
		return nil, err
	}
	if len(file.Known) == 0 {
		return nil, fmt.Errorf("closed-ticket manifest: %s has no known ticket list; the gate cannot tell a "+
			"plan ticket from a typo without it", path)
	}
	known := idSet(file.Known)
	for _, id := range file.Tickets {
		if !known[id] {
			return nil, fmt.Errorf("closed-ticket manifest: %s lists %q as closed but not as known", path, id)
		}
	}
	return known, nil
}

// capabilityIDPattern is one line of testdata/capability-ids.txt.
var capabilityIDPattern = regexp.MustCompile(`^cap:[a-z0-9-]+$`)

// LoadCapabilityIDs reads the capability ids file at path, one full id per
// line. A missing file, a file with no ids, or a line that is not a
// capability id is an error: an empty set would refuse nothing it should.
func LoadCapabilityIDs(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("capability ids: reading %s: %w (tracked and required; a missing file fails "+
			"the gate)", path, err)
	}
	out := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if !capabilityIDPattern.MatchString(line) {
			return nil, fmt.Errorf("capability ids: %s line %d is %q, not a capability id", path, i+1, line)
		}
		out[line] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("capability ids: %s lists no capability", path)
	}
	return out, nil
}

// CheckRetireKeys returns one sorted line per allow entry whose keys name
// something the plan export does not: a plan id missing from known (in
// retire_ticket or added_by_ticket), a capability id missing from caps, an
// unknown expires, or an expired cap row (its expires ticket is closed).
// UNOWNED rows are counted elsewhere. Shapes are the loader's job.
func CheckRetireKeys(allow map[string]TestOnlyAllowEntry, known, caps, closed map[string]bool) []string {
	var out []string
	for key, e := range allow {
		rt := e.RetireTicket
		switch {
		case rt == UnownedTicket:
		case IsCapKey(rt):
			if !caps[rt] {
				out = append(out, fmt.Sprintf("%s: retire_ticket %q is not a listed capability id", key, rt))
			}
			if !known[e.Expires] {
				out = append(out, fmt.Sprintf("%s: expires %q is not a known plan ticket", key, e.Expires))
			} else if closed[e.Expires] {
				out = append(out, fmt.Sprintf("%s: expired, its expires ticket %s is closed; wire the symbol "+
					"or delete the row", key, e.Expires))
			}
		case !known[rt]:
			out = append(out, fmt.Sprintf("%s: retire_ticket %q is not a known plan ticket", key, rt))
		}
		if e.AddedByTicket != "" && !known[e.AddedByTicket] {
			out = append(out, fmt.Sprintf("%s: added_by_ticket %q is not a known plan ticket", key, e.AddedByTicket))
		}
	}
	sort.Strings(out)
	return out
}

// CheckCountBaseline reads the single integer baseline at path and refuses
// got above it. A missing or malformed baseline is an error. The baseline is
// returned so a caller can report a count that fell below it.
func CheckCountBaseline(path string, got int) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("count baseline: reading %s: %w", path, err)
	}
	baseline, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("count baseline: %s must hold a single integer: %w", path, err)
	}
	if got > baseline {
		return baseline, fmt.Errorf("count %d is above the shrink-only baseline %d in %s", got, baseline, path)
	}
	return baseline, nil
}

// OrphanedAllowEntry is one allow-list entry whose retire_ticket is
// already closed and whose caller_site was never created. "Closed ticket,
// missing file" is one predicate, computed once here and consumed both by
// the orphan-refusal gate and by the tracked debt count.
type OrphanedAllowEntry struct {
	Symbol       string
	RetireTicket string
	CallerSite   string
}

// FindOrphanedAllowEntries returns every allow entry whose retire_ticket
// is in closed, or is a capability id (always closed), and whose caller_site
// file does not exist under moduleRoot, sorted by symbol. UNOWNED entries
// are never orphaned: they name no ticket to close. An entry whose
// caller_site file DOES exist is not orphaned even if CheckCallerSitesWired
// finds it does not reference the symbol; that is a different, already
// caught failure.
func FindOrphanedAllowEntries(
	moduleRoot string, allow map[string]TestOnlyAllowEntry, closed map[string]bool,
) []OrphanedAllowEntry {
	var out []OrphanedAllowEntry
	for key, e := range allow {
		if e.RetireTicket == UnownedTicket {
			continue
		}
		if !closed[e.RetireTicket] && !IsCapKey(e.RetireTicket) {
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
// depth, not the only backstop: the orphan predicate above, run at ship
// time against a manifest that already lists the shipping ticket as
// closed, catches a self-referential entry when its own ticket closes.
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

// TrackedDeadSurfaceCount is UNOWNED entries plus orphaned ones, counted
// together so an honest lane that left a gap findable (UNOWNED) is never
// worse off than one that named a since-closed ticket. Capability-keyed rows
// reach it only through the orphan predicate, never as a separate term.
func TrackedDeadSurfaceCount(allow map[string]TestOnlyAllowEntry, orphaned []OrphanedAllowEntry) int {
	return UnownedTicketCount(allow) + len(orphaned)
}
