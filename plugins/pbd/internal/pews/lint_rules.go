// Package pews (lint_rules.go): Purpose: the individual contract-lint
// rules N/S-28.T4 owns — is a loaded, structurally valid ticket's OWN
// CONTRACT well-formed and complete enough to build from, as opposed to
// validate.go's question of whether the tree it lives in is structurally
// sound. Each rule reads one already-decoded Ticket (plus, for the two
// tree-relative rules, a lookup of its siblings) and reports every
// violation it finds — never just the first.
// Inputs: TicketRecord values from an already-Validate-clean Tree (lint.go
// enforces that precondition); the two closed tables below (06-FORGE-SPEC
// §3's gate_only set and 12-QUALITY-CONSTITUTION Art.11's ten-ticket set)
// are reproduced here from spec prose because the tree has no field
// recording either membership.
// Outputs: []LintIssue per rule; lint.go concatenates and sorts them.
// Constraints: pure functions, no filesystem/clock/network access; the
// canonical-id ids below are built with canonicalID/epicNumberFromLetters
// (store.go) so a typo cannot silently diverge from the id format T2
// already enforces.
// SPORT: plugins/pbd/internal/pews lint_rules (ADD) — P1-E14-W3-S28-T4.
package pews

import (
	"fmt"
	"regexp"
	"strings"
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

// canonicalIDLoc names a ticket by tree location, letting the two closed
// tables below build canonical ids with the same canonicalID/
// epicNumberFromLetters helpers store.go uses, instead of hand-typing the
// "P1-E.." strings and risking a silent divergence.
type canonicalIDLoc struct {
	letters              string
	wave, sprint, ticket int
}

func (l canonicalIDLoc) id() string {
	return canonicalID("P1", epicNumberFromLetters(l.letters), l.wave, l.sprint, l.ticket)
}

// art11Locs is 12-QUALITY-CONSTITUTION.md Art.11's TEN-ticket hardening-
// clause restriction (R-16.27 as amended by R-21.20), reproduced from
// 06-FORGE-SPEC.md §5 rule 25's literal enumeration. Membership is closed:
// no eleventh ticket may carry the clause, and none of the ten may omit
// it.
var art11Locs = []canonicalIDLoc{
	{"D", 1, 7, 7}, {"I", 2, 18, 7}, {"N", 3, 30, 5}, {"S", 4, 42, 7},
	{"Y", 5, 52, 7}, {"AF", 6, 66, 5}, {"AJ", 7, 72, 6}, {"AM", 8, 76, 4},
	{"AR", 9, 85, 5}, {"AB", 10, 58, 7},
}

// gateOnlyLocs is 06-FORGE-SPEC.md §3's gate_only closed set: the tickets
// whose gate_only flag must read true, and the only tickets it may.
var gateOnlyLocs = []canonicalIDLoc{
	{"J", 4, 21, 3}, {"W", 6, 49, 4}, {"Q", 4, 38, 5}, {"S", 4, 42, 5},
	{"AB", 10, 58, 6}, {"AD", 6, 62, 4}, {"AF", 6, 66, 4}, {"AJ", 7, 72, 5},
	{"AM", 8, 76, 3}, {"AL", 6, 75, 3}, {"W", 6, 49, 3}, {"AN", 9, 78, 5},
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

// dodPhrases are the Article-3 Ticket DoD substrings 06-FORGE-SPEC.md §5
// rule 25 requires acceptance_criteria to carry verbatim enough to match
// on, joined across all acceptance_criteria entries.
var dodPhrases = []string{
	"checks green in CI", "error-path tests present", "-race clean",
	"no Article-1 stubs", "docs_updates landed", "CR by a different agent",
	"journal written",
}

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
	if len(t.Ticket.Title) > 60 {
		v = append(v, LintIssue{LintKindFieldTooLong, t.ID, t.RelPath,
			fmt.Sprintf("title is %d chars, 06 §1 field 2 caps it at 60", len(t.Ticket.Title))})
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

// ruleCRWeight checks 06 §1 field 14's weight -> cr_level floor: XS/S
// requires at least CR-A, M/L at least CR-B, XL at least CR-C. A
// security-class ticket may always add more (checked as "contains", not
// "equals"), so this is a floor, never an exact-match rule.
func ruleCRWeight(t TicketRecord) []LintIssue {
	floor := map[Weight]CRLevel{
		WeightXS: CRLevelA, WeightS: CRLevelA,
		WeightM: CRLevelB, WeightL: CRLevelB,
		WeightXL: CRLevelC,
	}
	want, ok := floor[t.Ticket.Weight]
	if !ok || strings.Contains(string(t.Ticket.CRLevel), string(want)) {
		return nil
	}
	return []LintIssue{{LintKindCRWeightMismatch, t.ID, t.RelPath,
		fmt.Sprintf("weight %s requires cr_level to include %s, got %s", t.Ticket.Weight, want, t.Ticket.CRLevel)}}
}

// ruleAcceptanceDoD checks that acceptance_criteria's combined text
// carries every 06 §5 rule 25 Article-3 Ticket DoD phrase.
func ruleAcceptanceDoD(t TicketRecord) []LintIssue {
	joined := strings.Join(t.Ticket.AcceptanceCriteria, "\n")
	for _, phrase := range dodPhrases {
		if !strings.Contains(joined, phrase) {
			return []LintIssue{{LintKindMissingDoD, t.ID, t.RelPath,
				fmt.Sprintf("acceptance_criteria is missing the Article-3 Ticket DoD phrase %q", phrase)}}
		}
	}
	return nil
}

// ruleFilesScope flags a files_scope with all three lists empty: a
// contract that touches nothing is not buildable.
func ruleFilesScope(t TicketRecord) []LintIssue {
	fs := t.Ticket.FilesScope
	if len(fs.Add) == 0 && len(fs.Change) == 0 && len(fs.Delete) == 0 {
		return []LintIssue{{LintKindFilesScopeEmpty, t.ID, t.RelPath, "files_scope declares no add, change, or delete entries"}}
	}
	return nil
}

// ruleJournals enforces 06 §1's "journals: true (always)" extra flag.
func ruleJournals(t TicketRecord) []LintIssue {
	if t.Ticket.Journals == nil || !*t.Ticket.Journals {
		return []LintIssue{{LintKindJournalsMissing, t.ID, t.RelPath, "journals must be set to true on every ticket"}}
	}
	return nil
}

// ruleGateOnly enforces 06 §3's gate_only closed set symmetrically: a
// member must carry gate_only: true, and gate_only: true is unauthorized
// on any id outside the set.
func ruleGateOnly(t TicketRecord, gateOnly map[string]bool) []LintIssue {
	is := t.Ticket.GateOnly != nil && *t.Ticket.GateOnly
	member := gateOnly[t.ID]
	switch {
	case is && !member:
		return []LintIssue{{LintKindGateOnlyUnexpected, t.ID, t.RelPath, "gate_only: true is not authorized on this ticket"}}
	case !is && member:
		return []LintIssue{{LintKindGateOnlyMissing, t.ID, t.RelPath, "this ticket is in 06 §3's gate_only set and must declare gate_only: true"}}
	}
	return nil
}

// ruleArt11 enforces Art.11's TEN-ticket closed set symmetrically: a
// member's acceptance_criteria must carry the Article-11 clause, and the
// clause is unauthorized on any ticket outside the set.
func ruleArt11(t TicketRecord, art11 map[string]bool) []LintIssue {
	joined := strings.ToLower(strings.Join(t.Ticket.AcceptanceCriteria, "\n"))
	has := strings.Contains(joined, "art.11") || strings.Contains(joined, "article 11") || strings.Contains(joined, "article-11")
	member := art11[t.ID]
	switch {
	case member && !has:
		return []LintIssue{{LintKindArt11Missing, t.ID, t.RelPath, "this ticket is in Art.11's TEN-ticket set and must carry the Article-11 acceptance clause"}}
	case !member && has:
		return []LintIssue{{LintKindArt11Unauthorized, t.ID, t.RelPath, "the Article-11 clause is unauthorized on any ticket outside the TEN-ticket set"}}
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
