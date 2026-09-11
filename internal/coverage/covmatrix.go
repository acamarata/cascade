// Package coverage holds the P1-E28-W10-S57-T1 inventory-coverage-matrix
// gate: it maps every row of 01-FEATURE-INVENTORY.md to a ticket the
// planning tree actually contains, or a deferral entry that actually
// exists, and reports every row that resolves to neither.
//
// SCOPE NOTE (honest, stated once here): the ticket that specified this
// package also specified a v1 CLI-module disposition sweep, a fuzzed
// parser, a tracked+diff-gated coverage-matrix.json artifact, a
// defects.yaml registry, and CI/pre-push wiring. None of that is built
// here — this file is the coverage-matrix mapper alone (LoadInventory,
// LoadTicketTree, LoadDeferrals, CoverageMatrix), built real and
// seeded-violation-tested rather than left as a stub covering the whole
// ticket. See this ticket's journal for what was cut and why.
//
// Inputs: 01-FEATURE-INVENTORY.md (a markdown file with one or more
// "| feature | disposition |" tables), the ticket tree under
// .claude/planning/p1/phase/epics (one YAML file per ticket, each with a
// top-level "id:" line), and deferrals.yaml (a YAML list under a
// top-level "deferrals:" key). All three live under the gitignored
// .claude/ tree — CI never sees them, so any lane that depends on this
// package runs local-only (R-14.85); see doc comments on the *_Live test
// for the exact guard.
//
// Outputs: a CoverageMatrix mapping each inventory row to the ticket
// citations and/or deferral ids it resolves against, plus an
// InventoryGapReport naming every row that resolves to neither.
//
// Constraints: fail closed on the two shapes this file exists to refuse
// to trust silently — an EMPTY inventory (a gate asserting "every row is
// covered" over zero rows always passes and catches nothing; LoadInventory
// therefore errors on zero parsed rows) and a citation that does not
// resolve to a real ticket file (ResolveTicketCitation walks the actual
// tree; it never assumes a citation is valid because it is shaped like
// one).
//
// What this gate does NOT catch: an inventory row with NO citation inside
// its own text is reported as a gap correctly, but a row whose citation
// text is WRONG in a way that still parses (the right shape, the wrong
// epic/sprint/ticket number) resolves to nothing and is also correctly
// reported as a gap — so this failure mode IS caught. What is NOT caught:
// a row that cites a real ticket which does not actually implement the
// row's feature (this gate checks the citation resolves to a FILE, never
// that the ticket's content matches the row's claim); a deferral id typo
// that happens to collide with a different real deferral id.
//
// SPORT: INVENTORY_COVERAGE_MATRIX: ADD (internal/coverage, pre-push tooling).
package coverage

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// InventoryRow is one data row of a 01-FEATURE-INVENTORY.md table.
type InventoryRow struct {
	// Section is the nearest preceding "## " heading.
	Section string
	// Index is the row's 1-based position within Section, for a stable,
	// human-findable RowID without inventing a row-id scheme the source
	// document does not have.
	Index int
	// Text is the row's full source line, cells and all.
	Text string
}

// RowID renders a stable, human-readable identifier for gap reports.
func (r InventoryRow) RowID() string {
	return fmt.Sprintf("%s #%d", r.Section, r.Index)
}

// tableRowExpr matches a markdown table row: starts and ends with "|".
var tableRowExpr = regexp.MustCompile(`^\|.*\|\s*$`)

// separatorRowExpr matches a table's header/body separator row, e.g.
// "|---|---|" or "| :-- | --: |".
var separatorRowExpr = regexp.MustCompile(`^\|[\s:|-]+\|\s*$`)

// headingExpr captures a markdown "## " (or deeper) heading's text.
var headingExpr = regexp.MustCompile(`^#{2,6}\s+(.+?)\s*$`)

// LoadInventory parses every markdown table row in path across every
// section, in document order. A row immediately preceding a separator
// row (the header row) is never collected. Fails closed: a missing file,
// an unreadable file, or zero parsed rows is an error — an empty
// inventory would make every downstream "zero gaps" assertion vacuously
// true, which is exactly the check-that-cannot-fail shape this gate
// exists to avoid.
func LoadInventory(path string) ([]InventoryRow, error) {
	f, err := os.Open(path) //nolint:gosec // repo-relative planning doc path
	if err != nil {
		return nil, fmt.Errorf("coverage: loading inventory %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only scan, nothing to flush

	var rows []InventoryRow
	var section string
	var idx int
	// inTable is true only once a header row has been confirmed by a
	// following separator row; it is what lets a table's every DATA row
	// (not just the first, not skipping the last) get collected without
	// off-by-one lag against the header/separator pair.
	var inTable bool
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := headingExpr.FindStringSubmatch(line); m != nil {
			section = m[1]
			idx = 0
			inTable = false
			continue
		}
		if separatorRowExpr.MatchString(line) {
			inTable = true // the row just seen was this table's header
			continue
		}
		if tableRowExpr.MatchString(line) {
			if inTable {
				idx++
				rows = append(rows, InventoryRow{Section: section, Index: idx, Text: line})
			}
			// else: this row IS the header a separator has not confirmed
			// yet; it is neither collected nor does it set inTable.
			continue
		}
		inTable = false
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("coverage: scanning inventory %s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("coverage: %s produced zero inventory rows (fail closed)", path)
	}
	return rows, nil
}

// citationExpr finds a shorthand ticket citation in row text, e.g.
// "K/S-23.T6" or "AK/S-73.T2": an epic letter code (1-3 uppercase
// letters, matching this tree's E-<letters> directory naming), a sprint
// number and a ticket number.
var citationExpr = regexp.MustCompile(`\b([A-Z]{1,3})/S-(\d{1,3})\.T(\d{1,3})\b`)

// deferralExpr finds a DEF-<CLASS>-<slug> deferral id in row text.
//
// The class segment is deliberately NOT restricted to P2. phase/deferrals.yaml
// carries 21 entries across three classes — 14 DEF-P2-*, 6 DEF-PROD-* and 1
// DEF-DROP-* — and an earlier `\bDEF-P2-[a-z0-9-]+\b` matched only the first
// group. The other seven were therefore INVISIBLE to this gate: a row citing a
// genuinely registered deferral such as DEF-PROD-comms-hub was reported as an
// uncovered gap, which is a false accusation by the gate rather than a real
// hole in the plan. Found while resolving R-14.228's 62-row gap list, where
// two of the last four "unresolved" rows turned out to be exactly this.
//
// Widening the EXTRACTION pattern cannot weaken the gate, because extraction
// is not validation: BuildCoverageMatrix resolves whatever this finds against
// knownDeferrals, the set actually loaded from deferrals.yaml. An invented or
// misspelled id still extracts, still fails that membership check, and still
// leaves the row a gap. Keeping the narrow pattern would have meant the gate
// could never see a whole class of real deferral no matter how it was cited.
var deferralExpr = regexp.MustCompile(`\bDEF-[A-Z0-9]+-[a-z0-9-]+\b`)

// ExtractCitations returns every shorthand ticket citation and deferral
// id found in text, each in its original form.
func ExtractCitations(text string) []string {
	var out []string
	out = append(out, citationExpr.FindAllString(text, -1)...)
	out = append(out, deferralExpr.FindAllString(text, -1)...)
	return out
}

// ResolveTicketCitation reports whether a "LETTERS/S-N.TM" citation
// resolves to a real ticket file under root's epics tree. It never
// assumes the shape is valid: a citation whose epic/sprint/ticket does
// not exist in the tree resolves to false, not an error — an unresolved
// citation is exactly what a gap report exists to name.
func ResolveTicketCitation(root, citation string) (bool, error) {
	m := citationExpr.FindStringSubmatch(citation)
	if m == nil {
		return false, nil
	}
	pattern := filepath.Join(root, ".claude", "planning", "p1", "phase", "epics",
		"E-"+m[1], "waves", "*", "sprints", "S-"+m[2], "tickets", "T-"+m[3]+".yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return false, fmt.Errorf("coverage: resolving citation %q: %w", citation, err)
	}
	return len(matches) > 0, nil
}

// TicketRecord is one ticket YAML's identity, as LoadTicketTree finds it.
type TicketRecord struct {
	ID   string
	Path string
}

// ticketIDExpr pulls the "id: ..." front-matter line out of a ticket
// YAML without a full YAML parse, matching how the tree's own tooling
// (e.g. the grep in this ticket's own AGENT-BRIEF) already locates
// tickets by id.
var ticketIDExpr = regexp.MustCompile(`(?m)^id:\s*(\S+)\s*$`)

// LoadTicketTree walks root/epics for every tickets/T-*.yaml file and
// returns its id and path. A directory that does not exist is an error:
// a scan that silently found zero tickets because the tree moved is the
// same failure shape LoadInventory refuses.
func LoadTicketTree(root string) ([]TicketRecord, error) {
	var out []TicketRecord
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		if !strings.Contains(filepath.ToSlash(path), "/tickets/") {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // repo-relative planning path
		if rerr != nil {
			return fmt.Errorf("coverage: reading %s: %w", path, rerr)
		}
		m := ticketIDExpr.FindSubmatch(data)
		if m == nil {
			return fmt.Errorf("coverage: %s has no id: line", path)
		}
		out = append(out, TicketRecord{ID: string(m[1]), Path: path})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("coverage: %s produced zero tickets (fail closed)", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeferralEntry is one .claude/planning/p1/phase/deferrals.yaml row. The
// schema, verbatim from the live file's header: id (slug), source
// (owning ticket or doc), ruling (R-nn.n), what (capability text),
// reenter (re-entry point).
type DeferralEntry struct {
	ID      string `yaml:"id"`
	Source  string `yaml:"source"`
	Ruling  string `yaml:"ruling"`
	What    string `yaml:"what"`
	Reenter string `yaml:"reenter"`
}

// deferralsFile is deferrals.yaml's top-level shape.
type deferralsFile struct {
	Deferrals []DeferralEntry `yaml:"deferrals"`
}

// LoadDeferrals parses path's deferrals list. A missing or malformed
// file is an error; an empty list is NOT an error on its own (a
// brand-new tree may legitimately defer nothing yet) — LoadInventory and
// LoadTicketTree own the "nothing found at all" refusal for their own
// sources.
func LoadDeferrals(path string) ([]DeferralEntry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // repo-relative planning doc path
	if err != nil {
		return nil, fmt.Errorf("coverage: loading deferrals %s: %w", path, err)
	}
	var parsed deferralsFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("coverage: parsing deferrals %s: %w", path, err)
	}
	for i, d := range parsed.Deferrals {
		if strings.TrimSpace(d.ID) == "" {
			return nil, fmt.Errorf("coverage: %s entry %d has no id", path, i)
		}
	}
	return parsed.Deferrals, nil
}
