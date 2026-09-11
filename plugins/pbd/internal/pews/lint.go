// Package pews (lint.go): Purpose: the contract-lint entry point N/S-28.T4
// owns. Validate (validate.go) answers "is this TREE structurally valid" —
// identity, duplicates, gaps, tombstones, dependency existence and
// cycles. Lint answers a different question about the SAME tree: "is
// each ticket's own CONTRACT well-formed and complete enough to build
// from" — the 17 fields present and meaningful, enums in the ranges 06
// §1/§3-§5 name, and the exact Constitution clauses those sections
// require. Lint never re-derives a structural fact Validate already
// owns: it COMPOSES Validate as its own precondition (see Lint below)
// rather than re-walking the tree for identity/duplicate/gap/dependency-
// existence/cycle checks.
// Inputs: a *Tree from store.go's Store.Load.
// Outputs: LintReport (every issue found, never just the first) plus a
// non-nil *cascade.Error of kind KindInvalidInput whenever the tree is
// structurally unsound OR LintReport carries at least one issue —
// fail-closed in both directions: an unparseable/invalid tree is a
// refusal, never a pass with a warning.
// Constraints: no bare time.Now/rand; iteration order is always sorted.
// SPORT: plugins/pbd/internal/pews lint (ADD) — P1-E14-W3-S28-T4.
package pews

import (
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LintReport is Lint's full result.
type LintReport struct {
	Issues      []LintIssue
	TicketCount int
}

// OK reports whether every ticket's contract lints clean.
func (r LintReport) OK() bool { return len(r.Issues) == 0 }

// Lint runs every contract-lint rule against tree and fails closed in two
// distinct ways. First, it requires tree to already be structurally valid
// per Validate — a tree Validate refuses is a lint refusal too, since a
// dangling dependency or a duplicate id makes several rules below
// (ruleGateOnly's membership lookup, ruleSubtickets' sibling lookup)
// unable to evaluate honestly; Lint composes Validate for this rather
// than re-implementing any of its checks. Second, once the tree is
// structurally sound, a non-empty LintReport also yields a non-nil error
// summarizing the count, exactly as Validate does for its own Report.
func Lint(tree *Tree) (LintReport, error) {
	if tree == nil {
		return LintReport{}, cascade.New(cascade.KindInvalidInput, "pews: cannot lint a nil tree")
	}
	if _, verr := Validate(tree); verr != nil {
		return LintReport{}, cascade.Wrap(cascade.KindInvalidInput, verr,
			"pews: contract lint requires a structurally valid tree; run pbd validate first")
	}

	byID := make(map[string]TicketRecord, len(tree.Tickets))
	for _, t := range tree.Tickets {
		byID[t.ID] = t
	}
	gateOnly := idSet(gateOnlyLocs)
	art11 := idSet(art11Locs)
	filesLess := idSet(filesLessLocs)

	var issues []LintIssue
	for _, t := range tree.Tickets {
		issues = append(issues, ruleFieldConstraints(t)...)
		issues = append(issues, ruleCardinality(t)...)
		issues = append(issues, ruleDependencyForm(t)...)
		issues = append(issues, ruleCRWeight(t)...)
		issues = append(issues, ruleAcceptanceDoD(t)...)
		issues = append(issues, ruleFilesScope(t, filesLess)...)
		issues = append(issues, ruleJournals(t)...)
		issues = append(issues, ruleGateOnly(t, gateOnly)...)
		issues = append(issues, ruleArt11(t, art11)...)
		issues = append(issues, ruleSubtickets(t, byID)...)
	}
	sortLintIssues(issues)

	report := LintReport{Issues: issues, TicketCount: len(tree.Tickets)}
	if len(issues) > 0 {
		return report, cascade.Newf(cascade.KindInvalidInput, "pews: %d contract-lint issue(s) found", len(issues))
	}
	return report, nil
}

func sortLintIssues(v []LintIssue) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].TicketID != v[j].TicketID {
			return v[i].TicketID < v[j].TicketID
		}
		return v[i].Kind < v[j].Kind
	})
}
