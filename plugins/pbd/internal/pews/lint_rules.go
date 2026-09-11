// Package pews (lint_rules.go): Purpose: the individual contract-lint
// rules N/S-28.T4 owns — is a loaded, structurally valid ticket's OWN
// CONTRACT well-formed and complete enough to build from, as opposed to
// validate.go's question of whether the tree it lives in is structurally
// sound. Each rule reads one already-decoded Ticket (plus, for the one
// tree-relative rule here, a lookup of its siblings) and reports every
// violation it finds — never just the first. The closed-set membership
// rules (gate_only, Art.11, files-scope exemption, cr_level rank) live
// in lint_rules_sets.go; the Article-3 Ticket DoD clause rule lives in
// lint_rules_dod.go — split out to keep this file under the 300-line cap.
// Inputs: TicketRecord values from an already-Validate-clean Tree (lint.go
// enforces that precondition).
// Outputs: []LintIssue per rule; lint.go concatenates and sorts them.
// Constraints: pure functions, no filesystem/clock/network access.
// SPORT: plugins/pbd/internal/pews lint_rules (ADD) — P1-E14-W3-S28-T4.
package pews

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// LintIssueKind names the class of one contract-completeness finding.
// Distinct from ViolationKind (validate.go): a ViolationKind is about tree
// structure, a LintIssueKind is about one ticket's own contract content.
type LintIssueKind string

// The closed set of lint-issue kinds Lint can report.
const (
	LintKindFieldRequired      LintIssueKind = "field-required"
	LintKindFieldTooLong       LintIssueKind = "field-too-long"
	LintKindCardinality        LintIssueKind = "cardinality"
	LintKindBlankEntry         LintIssueKind = "blank-entry"
	LintKindDependencyForm     LintIssueKind = "dependency-form"
	LintKindCRWeightMismatch   LintIssueKind = "cr-weight-mismatch"
	LintKindMissingDoD         LintIssueKind = "missing-dod-clause"
	LintKindFilesScopeEmpty    LintIssueKind = "files-scope-empty"
	LintKindJournalsMissing    LintIssueKind = "journals-missing"
	LintKindGateOnlyUnexpected LintIssueKind = "gate-only-unauthorized"
	LintKindGateOnlyMissing    LintIssueKind = "gate-only-missing"
	LintKindSubticketDangling  LintIssueKind = "subticket-dangling"
	LintKindSubticketOverlap   LintIssueKind = "subticket-files-overlap"
	LintKindArt11Missing       LintIssueKind = "art11-missing"
	LintKindArt11Unauthorized  LintIssueKind = "art11-unauthorized"
)

// LintIssue is one contract-completeness finding.
type LintIssue struct {
	Kind     LintIssueKind
	TicketID string
	Path     string
	Message  string
}

// canonicalIDLoc names a ticket by tree location, letting the closed
// tables in lint_rules_sets.go build canonical ids with the same
// canonicalID/epicNumberFromLetters helpers store.go uses, instead of
// hand-typing the "P1-E.." strings and risking a silent divergence.
type canonicalIDLoc struct {
	letters              string
	wave, sprint, ticket int
}

func (l canonicalIDLoc) id() string {
	return canonicalID("P1", epicNumberFromLetters(l.letters), l.wave, l.sprint, l.ticket)
}

func idSet(locs []canonicalIDLoc) map[string]bool {
	set := make(map[string]bool, len(locs))
	for _, l := range locs {
		set[l.id()] = true
	}
	return set
}

var dependencyIDRe = regexp.MustCompile(`^P[0-9]+-E[0-9]+-W[0-9]+-S[0-9]+-T[0-9]+$`)
var branchRe = regexp.MustCompile(`^P[0-9]+-E[A-Za-z0-9]+-W[0-9]+-S[0-9]+-T[0-9]+-[A-Za-z0-9-]+$`)

// ruleFieldConstraints checks the four free-text 06 §1 fields that carry
// no enum (title, short_desc, full_desc, branch): non-empty, and title's
// 06 §1 field-2 length cap.
func ruleFieldConstraints(t TicketRecord) []LintIssue {
	var v []LintIssue
	req := func(name, val string) {
		if strings.TrimSpace(val) == "" {
			v = append(v, LintIssue{LintKindFieldRequired, t.ID, t.RelPath, fmt.Sprintf("%s must not be empty", name)})
		}
	}
	req("title", t.Ticket.Title)
	req("short_desc", t.Ticket.ShortDesc)
	req("full_desc", t.Ticket.FullDesc)
	req("branch", t.Ticket.Branch)
	// utf8.RuneCountInString, never len(): 06 §1 field 2's "≤60 chars" is a
	// human character count, and titles routinely carry multi-byte runes
	// (·, →) that len() (byte count) over-counts, flagging a 57-character
	// title as 61 "chars" for carrying two 3-byte arrows.
	if n := utf8.RuneCountInString(t.Ticket.Title); n > 60 {
		v = append(v, LintIssue{LintKindFieldTooLong, t.ID, t.RelPath,
			fmt.Sprintf("title is %d chars, 06 §1 field 2 caps it at 60", n)})
	}
	if t.Ticket.Branch != "" && !branchRe.MatchString(t.Ticket.Branch) {
		v = append(v, LintIssue{LintKindFieldRequired, t.ID, t.RelPath,
			fmt.Sprintf("branch %q does not match the P1-EX-Wn-Snn-Tn-Kebab-Title shape", t.Ticket.Branch)})
	}
	return v
}

// ruleCardinality checks 06 §1's list-field cardinalities (tasks 3-9;
// checks/acceptance_criteria/spec_refs non-empty) and flags any blank
// entry in those four lists plus files_scope's three.
func ruleCardinality(t TicketRecord) []LintIssue {
	var v []LintIssue
	if n := len(t.Ticket.Tasks); n < 3 || n > 9 {
		v = append(v, LintIssue{LintKindCardinality, t.ID, t.RelPath,
			fmt.Sprintf("tasks has %d entries, 06 §1 field 9 requires 3-9", n)})
	}
	need := func(name string, list []string) {
		if len(list) == 0 {
			v = append(v, LintIssue{LintKindCardinality, t.ID, t.RelPath, fmt.Sprintf("%s must not be empty", name)})
		}
	}
	need("checks", t.Ticket.Checks)
	need("acceptance_criteria", t.Ticket.AcceptanceCriteria)
	need("spec_refs", t.Ticket.SpecRefs)
	blank := func(name string, list []string) {
		for i, s := range list {
			if strings.TrimSpace(s) == "" {
				v = append(v, LintIssue{LintKindBlankEntry, t.ID, t.RelPath, fmt.Sprintf("%s[%d] is blank", name, i)})
			}
		}
	}
	blank("tasks", t.Ticket.Tasks)
	blank("checks", t.Ticket.Checks)
	blank("acceptance_criteria", t.Ticket.AcceptanceCriteria)
	blank("spec_refs", t.Ticket.SpecRefs)
	return v
}

// ruleDependencyForm flags any depends_on entry that is not already a
// literal canonical ticket id. 06 §3's shorthand forms (S-nn.Tn, ranges,
// epic letters, "all") are the FORGE worker's job to resolve before the
// contract ships; by lint time every entry must already be literal, which
// is a completeness question this ticket owns and store.go's dangling-
// dependency check (an existence lookup, not a shape check) does not.
func ruleDependencyForm(t TicketRecord) []LintIssue {
	var v []LintIssue
	for i, dep := range t.Ticket.DependsOn {
		if !dependencyIDRe.MatchString(dep) {
			v = append(v, LintIssue{LintKindDependencyForm, t.ID, t.RelPath,
				fmt.Sprintf("depends_on[%d] %q is not a literal ticket id; forge must resolve shorthand before the contract ships", i, dep)})
		}
	}
	return v
}

// ruleJournals enforces 06 §1's "journals: true (always)" extra flag.
func ruleJournals(t TicketRecord) []LintIssue {
	if t.Ticket.Journals == nil || !*t.Ticket.Journals {
		return []LintIssue{{LintKindJournalsMissing, t.ID, t.RelPath, "journals must be set to true on every ticket"}}
	}
	return nil
}

// ruleSubtickets checks the subtickets extra flag's two applicability
// conditions: every named id exists in the tree, and the named siblings'
// files_scope entries are pairwise disjoint (06 §1: "only when files_scope
// is disjoint").
func ruleSubtickets(t TicketRecord, byID map[string]TicketRecord) []LintIssue {
	var v []LintIssue
	seen := map[string]string{}
	for _, sub := range t.Ticket.Subtickets {
		rec, ok := byID[sub]
		if !ok {
			v = append(v, LintIssue{LintKindSubticketDangling, t.ID, t.RelPath,
				fmt.Sprintf("subtickets entry %q is not a ticket id in this tree", sub)})
			continue
		}
		for _, f := range allFiles(rec.Ticket.FilesScope) {
			if owner, dup := seen[f]; dup {
				v = append(v, LintIssue{LintKindSubticketOverlap, t.ID, t.RelPath,
					fmt.Sprintf("subtickets %q and %q both claim files_scope path %q", owner, sub, f)})
				continue
			}
			seen[f] = sub
		}
	}
	return v
}

// allFiles flattens a FilesScope's three lists into one slice.
func allFiles(fs FilesScope) []string {
	out := make([]string, 0, len(fs.Add)+len(fs.Change)+len(fs.Delete))
	out = append(out, fs.Add...)
	out = append(out, fs.Change...)
	out = append(out, fs.Delete...)
	return out
}
