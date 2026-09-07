// Package pews (validate.go): the native structural validator over a
// loaded Tree, matching 06-FORGE-SPEC.md §3/§8: canonical ticket identity,
// duplicate/gapped ticket ids, tombstone bookkeeping, literal dependency
// targets (tombstones are never dep targets), and dependency cycles.
// Inputs: a *Tree from store.go's Store.Load; no filesystem/clock/network
// access of its own. Outputs: Report (every violation found, never just
// the first, plus active/tombstone/gate-only counts) plus a non-nil
// *cascade.Error of kind KindInvalidInput whenever Report carries at
// least one violation — fail-closed: a caller checking only the error
// still refuses. Constraints: no bare time.Now/rand; iteration order is
// always sorted, so output never depends on map order.
// SPORT: plugins/pbd/internal/pews validate (ADD) — P1-E14-W3-S28-T2.
package pews

import (
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ViolationKind names the class of one structural refusal. Callers switch
// on this rather than pattern-matching Message strings.
type ViolationKind string

// The closed set of violation kinds Validate can report.
const (
	ViolationIdentityMismatch ViolationKind = "identity-mismatch"
	ViolationDuplicateID      ViolationKind = "duplicate-id"
	ViolationGap              ViolationKind = "gap"
	ViolationTombstoneLive    ViolationKind = "tombstone-still-active"
	ViolationDuplicateTomb    ViolationKind = "duplicate-tombstone"
	ViolationDanglingDep      ViolationKind = "dangling-dependency"
	ViolationTombstoneDep     ViolationKind = "tombstone-dependency"
	ViolationCycle            ViolationKind = "dependency-cycle"
)

// Violation is one structural refusal Validate found.
type Violation struct {
	Kind     ViolationKind
	TicketID string
	Path     string
	Message  string
}

// Report is Validate's full result. Draft mirrors the tree's own Draft
// field (R-21.276): true whenever the phase record says draft: true,
// regardless of whether its tickets were included. A default (excluded)
// Load always produces ActiveCount 0 alongside Draft true for a draft
// phase, never a draft ticket's structural facts folded silently into a
// populated active report.
type Report struct {
	Violations     []Violation
	ActiveCount    int
	TombstoneCount int
	GateOnlyCount  int
	Draft          bool
}

// OK reports whether the tree validated clean.
func (r Report) OK() bool { return len(r.Violations) == 0 }

// Validate runs every structural check against tree and fails closed:
// whenever Report.Violations is non-empty, Validate also returns a
// non-nil *cascade.Error summarizing the count. Validate never panics and
// never partially populates Report on a nil tree.
func Validate(tree *Tree) (Report, error) {
	if tree == nil {
		return Report{}, cascade.New(cascade.KindInvalidInput, "pews: cannot validate a nil tree")
	}

	byID, dupIDs := indexByID(tree.Tickets)
	tombByID, dupTombs := indexTombstones(tree.Tombstones)

	var v []Violation
	v = append(v, identityViolations(tree.Tickets)...)
	v = append(v, dupIDs...)
	v = append(v, dupTombs...)
	v = append(v, sequenceViolations(tree.Phase, tree.Tickets, tombByID)...)
	v = append(v, tombstoneLiveViolations(tree.Tickets, tombByID)...)
	v = append(v, dependencyViolations(tree.Tickets, byID, tombByID)...)
	v = append(v, cycleViolations(tree.Tickets, byID)...)
	sortViolations(v)

	report := Report{
		Violations:     v,
		ActiveCount:    len(tree.Tickets),
		TombstoneCount: len(tree.Tombstones),
		GateOnlyCount:  countGateOnly(tree.Tickets),
		Draft:          tree.Draft,
	}
	if len(v) > 0 {
		return report, cascade.Newf(cascade.KindInvalidInput, "pews: %d structural violation(s) found", len(v))
	}
	return report, nil
}

func sortViolations(v []Violation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].TicketID != v[j].TicketID {
			return v[i].TicketID < v[j].TicketID
		}
		return v[i].Kind < v[j].Kind
	})
}

// indexByID builds the declared-id lookup table, reporting every id
// declared more than once as a ViolationDuplicateID (the first occurrence
// wins the map entry; later ones are reported, not silently dropped).
func indexByID(tickets []TicketRecord) (map[string]TicketRecord, []Violation) {
	byID := make(map[string]TicketRecord, len(tickets))
	var v []Violation
	for _, t := range tickets {
		if _, dup := byID[t.ID]; dup {
			v = append(v, Violation{Kind: ViolationDuplicateID, TicketID: t.ID, Path: t.RelPath,
				Message: fmt.Sprintf("ticket id %q declared more than once", t.ID)})
			continue
		}
		byID[t.ID] = t
	}
	return byID, v
}

// indexTombstones builds the tombstone lookup table, reporting a repeated
// tombstone id as ViolationDuplicateTomb.
func indexTombstones(list []Tombstone) (map[string]Tombstone, []Violation) {
	byID := make(map[string]Tombstone, len(list))
	var v []Violation
	for _, tb := range list {
		if _, dup := byID[tb.ID]; dup {
			v = append(v, Violation{Kind: ViolationDuplicateTomb, TicketID: tb.ID,
				Message: fmt.Sprintf("tombstone id %q recorded more than once", tb.ID)})
			continue
		}
		byID[tb.ID] = tb
	}
	return byID, v
}

// identityViolations reports every ticket whose declared id does not
// match the id its tree position implies.
func identityViolations(tickets []TicketRecord) []Violation {
	var v []Violation
	for _, t := range tickets {
		if t.ID != t.CanonicalID {
			v = append(v, Violation{Kind: ViolationIdentityMismatch, TicketID: t.CanonicalID, Path: t.RelPath,
				Message: fmt.Sprintf("ticket at %s declares id %q, tree position implies %q", t.RelPath, t.ID, t.CanonicalID)})
		}
	}
	return v
}

// sequenceGroup keys one epic/wave/sprint's ticket-number sequence.
type sequenceGroup struct{ epic, wave, sprint int }

// sequenceViolations reports every gap in a sprint's 1..max ticket-number
// sequence that is not covered by a recorded tombstone for that exact
// canonical id.
func sequenceViolations(phase string, tickets []TicketRecord, tombByID map[string]Tombstone) []Violation {
	groups := map[sequenceGroup][]int{}
	for _, t := range tickets {
		k := sequenceGroup{t.EpicNum, t.Wave, t.Sprint}
		groups[k] = append(groups[k], t.TicketNum)
	}
	keys := make([]sequenceGroup, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].epic != keys[j].epic {
			return keys[i].epic < keys[j].epic
		}
		if keys[i].wave != keys[j].wave {
			return keys[i].wave < keys[j].wave
		}
		return keys[i].sprint < keys[j].sprint
	})

	var v []Violation
	for _, k := range keys {
		present := map[int]bool{}
		maxNum := 0
		for _, n := range groups[k] {
			present[n] = true
			if n > maxNum {
				maxNum = n
			}
		}
		for n := 1; n <= maxNum; n++ {
			if present[n] {
				continue
			}
			missing := canonicalID(phase, k.epic, k.wave, k.sprint, n)
			if _, tomb := tombByID[missing]; tomb {
				continue
			}
			v = append(v, Violation{Kind: ViolationGap, TicketID: missing,
				Message: fmt.Sprintf("ticket number %d missing from epic %d/wave %d/sprint %d with no matching tombstone", n, k.epic, k.wave, k.sprint)})
		}
	}
	return v
}

// tombstoneLiveViolations reports every ticket id that is both recorded as
// tombstoned and still present as a live ticket file.
func tombstoneLiveViolations(tickets []TicketRecord, tombByID map[string]Tombstone) []Violation {
	var v []Violation
	for _, t := range tickets {
		if _, ok := tombByID[t.ID]; ok {
			v = append(v, Violation{Kind: ViolationTombstoneLive, TicketID: t.ID, Path: t.RelPath,
				Message: fmt.Sprintf("ticket id %q is recorded as tombstoned but a live ticket file still declares it", t.ID)})
		}
	}
	return v
}

// dependencyViolations reports every depends_on entry that names a
// tombstoned id (ViolationTombstoneDep — tombstones are never dep
// targets) or an id present nowhere in the tree (ViolationDanglingDep).
func dependencyViolations(tickets []TicketRecord, byID map[string]TicketRecord, tombByID map[string]Tombstone) []Violation {
	var v []Violation
	for _, t := range tickets {
		for _, dep := range t.Ticket.DependsOn {
			if _, tomb := tombByID[dep]; tomb {
				v = append(v, Violation{Kind: ViolationTombstoneDep, TicketID: t.ID, Path: t.RelPath,
					Message: fmt.Sprintf("ticket %q depends on tombstoned id %q", t.ID, dep)})
				continue
			}
			if _, ok := byID[dep]; !ok {
				v = append(v, Violation{Kind: ViolationDanglingDep, TicketID: t.ID, Path: t.RelPath,
					Message: fmt.Sprintf("ticket %q depends on unknown id %q", t.ID, dep)})
			}
		}
	}
	return v
}

// cycleViolations runs a deterministic (sorted-id-order) DFS over the
// depends_on graph restricted to resolvable edges (a dangling or
// tombstone-target edge is already reported elsewhere and is never part of
// a reported cycle), reporting each cycle exactly once at the ticket where
// the DFS re-entered a still-open (gray) node.
func cycleViolations(tickets []TicketRecord, byID map[string]TicketRecord) []Violation {
	const white, gray, black = 0, 1, 2
	color := make(map[string]int, len(tickets))
	var stack []string
	var v []Violation

	var visit func(id string)
	visit = func(id string) {
		color[id] = gray
		stack = append(stack, id)
		if rec, ok := byID[id]; ok {
			for _, dep := range rec.Ticket.DependsOn {
				if _, exists := byID[dep]; !exists {
					continue
				}
				switch color[dep] {
				case white:
					visit(dep)
				case gray:
					path := append(append([]string{}, stack...), dep)
					v = append(v, Violation{Kind: ViolationCycle, TicketID: id,
						Message: fmt.Sprintf("dependency cycle: %s", strings.Join(path, " -> "))})
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}

	ids := make([]string, 0, len(tickets))
	for _, t := range tickets {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			visit(id)
		}
	}
	return v
}

// countGateOnly counts tickets whose gate_only flag is explicitly true.
func countGateOnly(tickets []TicketRecord) int {
	n := 0
	for _, t := range tickets {
		if t.Ticket.GateOnly != nil && *t.Ticket.GateOnly {
			n++
		}
	}
	return n
}
