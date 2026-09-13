// Package main is a LOCAL-ONLY generator: it regenerates
// internal/build/testdata/closed-ticket-manifest.json from the real
// .claude/planning/p1/phase/journals directory, which exists only on a
// developer's own checkout (.claude/ is gitignored; see
// internal/build/orphangate.go's doc comment for the full CI-visibility
// reasoning behind this split). Run it via
// `go run ./internal/build/gen/closedtickets` from anywhere inside the
// repo (it resolves the repo root the same way internal/inventory/gen
// does) whenever a ticket closes, i.e. whenever a journal lands under
// journals/. CI never runs this: it has no journals directory and reads
// only the tracked snapshot this generator produces.
//
// This generator also realizes
// DEFECT-orphaned-allow-entries-systemic.md's gate 3 ("fail when a
// ticket CLOSES while still named as a live retire_ticket by some
// entry"): before writing the manifest, it checks whether adding any
// newly-closed ticket would orphan an allow-list entry that is not
// already on the grandfathered list
// (internal/build/testdata/testonly-orphan-grandfather.txt). If so, it
// refuses to write and reports which entries, so a ticket cannot close
// silently over a hiding place it just created.
//
// Inputs: .claude/planning/p1/phase/journals/*.md (ticket ids, matched
// against the exact filename shape a real journal uses),
// internal/build/testonly-allow.json,
// internal/build/testdata/testonly-orphan-grandfather.txt.
// Outputs: internal/build/testdata/closed-ticket-manifest.json.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

func main() {
	w := output.NewDefault(false, false, false, false)
	root, err := repoRoot()
	if err != nil {
		w.Fail(err)
		os.Exit(1)
	}
	if err := run(w, root, runtime.NewSystemClock()); err != nil {
		w.Fail(err)
		os.Exit(1)
	}
}

// journalTicketPattern matches the exact filename shape a closed
// ticket's journal uses: journals/<TICKET-ID>.md, no prefix or suffix. A
// BLOCKED-, DEFECT-, FIX- or investigative journal never counts as a
// ticket closure: a journal means done only when it names nothing but
// the ticket itself (AGENT-BRIEF, "A JOURNAL MEANS DONE").
var journalTicketPattern = regexp.MustCompile(`^(P1-E\d{1,3}-W\d{1,2}-S\d{1,3}-T\d{1,3})\.md$`)

// scanClosedTickets returns every ticket id with a real, bare journal
// under journalsDir, sorted. A missing journals directory is an error,
// not an empty result, so the caller can distinguish "not this checkout"
// from "regenerate to zero tickets", which this tree has never actually
// been in.
func scanClosedTickets(journalsDir string) ([]string, error) {
	entries, err := os.ReadDir(journalsDir)
	if err != nil {
		return nil, fmt.Errorf("closedtickets: reading %s: %w", journalsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := journalTicketPattern.FindStringSubmatch(e.Name()); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

// loadGrandfatherList reads the shrink-only orphan grandfather list: one
// allow-list symbol key per line.
func loadGrandfatherList(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("closedtickets: reading grandfather list %s: %w", path, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out[s] = true
		}
	}
	return out, nil
}

// run performs the full regenerate-or-refuse cycle against root.
func run(w *output.Writer, root string, clock runtime.Clock) error {
	closedList, err := scanClosedTickets(filepath.Join(root, ".claude", "planning", "p1", "phase", "journals"))
	if err != nil {
		return err
	}

	allow, err := build.LoadTestOnlyAllowList(filepath.Join(root, "internal", "build", "testonly-allow.json"))
	if err != nil {
		return err
	}
	grandfather, err := loadGrandfatherList(
		filepath.Join(root, "internal", "build", "testdata", "testonly-orphan-grandfather.txt"))
	if err != nil {
		return err
	}

	if err := refuseIfNewOrphans(root, allow, closedList, grandfather); err != nil {
		return err
	}

	outPath := filepath.Join(root, "internal", "build", "testdata", "closed-ticket-manifest.json")
	data, err := renderManifest(closedList, clock)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil { //nolint:gosec // generated artifact, not a secret
		return fmt.Errorf("closedtickets: writing %s: %w", outPath, err)
	}
	w.Println("wrote", outPath, "with", len(closedList), "closed tickets")
	return nil
}

// refuseIfNewOrphans is DEFECT-orphaned-allow-entries-systemic.md's gate
// 3: a ticket may not close while still named as a live retire_ticket by
// an allow entry whose caller_site was never created, unless that entry
// is already on the grandfathered list. It reuses
// build.FindOrphanedAllowEntries rather than reimplementing the
// predicate a second time.
func refuseIfNewOrphans(
	root string, allow map[string]build.TestOnlyAllowEntry, closedList []string, grandfather map[string]bool,
) error {
	closedSet := make(map[string]bool, len(closedList))
	for _, t := range closedList {
		closedSet[t] = true
	}
	var fresh []build.OrphanedAllowEntry
	for _, o := range build.FindOrphanedAllowEntries(root, allow, closedSet) {
		if !grandfather[o.Symbol] {
			fresh = append(fresh, o)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	var b strings.Builder
	for _, o := range fresh {
		fmt.Fprintf(&b, "\n  %s -> retire_ticket %s is now closed, caller_site %s was never created",
			o.Symbol, o.RetireTicket, o.CallerSite)
	}
	return fmt.Errorf("closedtickets: refusing to regenerate the manifest: %d entr(y/ies) would become a NEW "+
		"orphan not already grandfathered:%s\n\nEither wire the symbol, correct its caller_site, or add it to "+
		"testdata/testonly-orphan-grandfather.txt with a reason in the ticket's journal. A ticket may not close "+
		"over a hiding place it just created", len(fresh), b.String())
}

// renderManifest serializes closedList deterministically, matching
// testdata/closed-ticket-manifest.json's tracked shape.
func renderManifest(closedList []string, clock runtime.Clock) ([]byte, error) {
	m := struct {
		GeneratedAt string   `json:"generated_at"`
		Tickets     []string `json:"tickets"`
	}{
		GeneratedAt: clock.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Tickets:     closedList,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("closedtickets: rendering manifest: %w", err)
	}
	return append(data, '\n'), nil
}

// repoRoot resolves the checkout root, the same way internal/inventory/gen
// does.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
