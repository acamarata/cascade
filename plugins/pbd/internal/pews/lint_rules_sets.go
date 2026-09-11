// Package pews (lint_rules_sets.go): Purpose: the contract-lint rules
// that check membership in a closed, spec-enumerated set of tickets —
// 06-FORGE-SPEC.md §3's gate_only set, 12-QUALITY-CONSTITUTION Art.11's
// ten-ticket hardening-clause set, and the two-ticket files_scope
// exemption for pure release-execution tickets — plus the cr_level
// rank-vs-weight floor check. Split out of lint_rules.go to keep that
// file under the 300-line cap; part of the same rule set lint.go calls.
// Inputs: TicketRecord values from an already-Validate-clean Tree.
// Outputs: []LintIssue per rule; lint.go concatenates and sorts them.
// Constraints: pure functions, no filesystem/clock/network access; the
// canonical-id ids below are built with canonicalID/epicNumberFromLetters
// (store.go) so a typo cannot silently diverge from the id format T2
// already enforces.
// SPORT: plugins/pbd/internal/pews lint_rules (ADD) — P1-E14-W3-S28-T4.
package pews

import (
	"fmt"
	"strings"
)

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
// whose gate_only flag must read true, and the only tickets it may. Wave
// numbers are each ticket's REAL tree location, verified against the
// on-disk epics/E-*/waves/ layout, not a guessed per-epic default: J/S-21
// lives in W3, W/S-49 lives in W5, and AL/S-75 lives in W8. Three of the
// four members below carried a wrong wave and were verified against disk
// before this fix; the ids/sprint/ticket numbers already matched.
var gateOnlyLocs = []canonicalIDLoc{
	{"J", 3, 21, 3}, {"W", 5, 49, 4}, {"Q", 4, 38, 5}, {"S", 4, 42, 5},
	{"AB", 10, 58, 6}, {"AD", 6, 62, 4}, {"AF", 6, 66, 4}, {"AJ", 7, 72, 5},
	{"AM", 8, 76, 3}, {"AL", 8, 75, 3}, {"W", 5, 49, 3}, {"AN", 9, 78, 5},
}

// filesLessLocs is the closed, two-ticket set of pure release-execution
// tickets whose entire job is cutting a git tag and publishing/
// announcing already-built artifacts to infrastructure outside this
// repo (registries, brew tap, GitHub release) — AA/S-56.T4 (rc cut) and
// AA/S-56.T6 (final promotion). Their tasks/checks touch zero source
// files by design, unlike every other ticket in the tree (which is why
// this is a two-entry closed set, not a general carve-out): a contract
// that changes no files is still not buildable UNLESS its entire
// contract is running an already-built release train.
var filesLessLocs = []canonicalIDLoc{
	{"AA", 10, 56, 4}, {"AA", 10, 56, 6},
}

// ruleFilesScope flags a files_scope with all three lists empty: a
// contract that touches nothing is not buildable, except filesLess'
// two closed-set release-execution tickets.
func ruleFilesScope(t TicketRecord, filesLess map[string]bool) []LintIssue {
	fs := t.Ticket.FilesScope
	if len(fs.Add) == 0 && len(fs.Change) == 0 && len(fs.Delete) == 0 && !filesLess[t.ID] {
		return []LintIssue{{LintKindFilesScopeEmpty, t.ID, t.RelPath, "files_scope declares no add, change, or delete entries"}}
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
// clause is unauthorized on any ticket outside the set. The ticket that
// implements THIS lint rule (N/S-28.T4) necessarily describes the
// closed set in its own acceptance_criteria as a test assertion ("the
// Article-11 restriction ... is the TEN-ticket set: D/S-07.T7, ...");
// that is documentation of the rule, never a self-claim of gate
// membership, so it is excluded via the same "ten-ticket set" wording
// the closed set's own doc comments above use, distinct from a real
// gate ticket's clause, which always asserts its own position (e.g.
// "the eighth of the ten") and never re-enumerates every member id.
func ruleArt11(t TicketRecord, art11 map[string]bool) []LintIssue {
	joined := strings.ToLower(strings.Join(t.Ticket.AcceptanceCriteria, "\n"))
	has := strings.Contains(joined, "art.11") || strings.Contains(joined, "article 11") || strings.Contains(joined, "article-11")
	has = has && !strings.Contains(joined, "ten-ticket set")
	member := art11[t.ID]
	switch {
	case member && !has:
		return []LintIssue{{LintKindArt11Missing, t.ID, t.RelPath, "this ticket is in Art.11's TEN-ticket set and must carry the Article-11 acceptance clause"}}
	case !member && has:
		return []LintIssue{{LintKindArt11Unauthorized, t.ID, t.RelPath, "the Article-11 clause is unauthorized on any ticket outside the TEN-ticket set"}}
	}
	return nil
}

// ruleCRWeight checks 06 §1 field 14's weight -> cr_level floor: XS/S
// requires at least CR-A rigor, M/L at least CR-B, XL at least CR-C.
// schema.go's own CRLevel doc calls CR-A/B/C a "strictly increasing"
// combination (join order CR-A, CR-B, CR-C), and 06 §4's model-class
// matrix names CR-B "review" and CR-C "arbiter" as successively heavier
// tiers, so a ticket carrying a HIGHER tier already satisfies a lower
// floor: a security-class XS/S ticket that upgrades to CR-B+CR-C (never
// invoking the lighter CR-A) has still met, and exceeded, the CR-A
// floor. This is therefore a rank comparison against the ticket's own
// highest declared tier, never a literal-token "contains" match — the
// literal check flagged every real heavy/arbiter security-class ticket
// in the tree that legitimately swapped CR-A for CR-C.
func ruleCRWeight(t TicketRecord) []LintIssue {
	floor := map[Weight]CRLevel{
		WeightXS: CRLevelA, WeightS: CRLevelA,
		WeightM: CRLevelB, WeightL: CRLevelB,
		WeightXL: CRLevelC,
	}
	want, ok := floor[t.Ticket.Weight]
	if !ok || crMaxRank(t.Ticket.CRLevel) >= crLevelIndex(want) {
		return nil
	}
	return []LintIssue{{LintKindCRWeightMismatch, t.ID, t.RelPath,
		fmt.Sprintf("weight %s requires cr_level rigor of at least %s, got %s", t.Ticket.Weight, want, t.Ticket.CRLevel)}}
}

// crMaxRank returns the highest crLevelIndex among c's '+'-joined parts,
// or -1 for an unparseable/empty CRLevel (never higher than any real
// floor, so it still fails the >= comparison above).
func crMaxRank(c CRLevel) int {
	rank := -1
	for _, part := range strings.Split(string(c), "+") {
		if idx := crLevelIndex(CRLevel(part)); idx > rank {
			rank = idx
		}
	}
	return rank
}
