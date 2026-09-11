// Package pews (residue.go): residue carry-forward (R-21.276) over the
// existing S-28 PEWS schema, tree store, and validator, through the
// S-29.T2 draft-phase boundary — no second schema or persistence path.
// Inputs: two PhaseID values (a tree root plus its ticket-id phase
// prefix, the same pair Store already takes); no clock, no network.
// Outputs: nil on success (dst gains one new ticket file per residue
// item plus a carried-forward.yaml provenance entry; src's originals are
// tombstoned and removed), or a *cascade.Error otherwise. A refusal
// because dst is not a draft phase is checked before any write and
// leaves both phases byte-for-byte untouched.
//
// Status caveat: R-21.276 defines residue as "tickets of a closing phase
// whose status is not done", but no status field exists anywhere in this
// engine yet: Ticket (schema.go, T1, a closed 17+5-field contract) has
// none, TicketRecord/Row (store.go/projector.go) add none, and status.go
// (N/S-29.T4) states it introduces "no invented status field... beyond
// pews.Row's own fields". Both are out of this ticket's files_scope.
// Residue is therefore every src ticket not already recorded in src's
// own tombstones.yaml, the one terminal-state signal the tree already
// persists; a real completion field remains a gap for a later ticket.
//
// carried_from caveat: R-21.276 also requires stamping "carried_from:
// <src-ticket-id>" on the copy itself. Ticket's field set is closed and
// fails closed on an unknown key (schema.go, T1, out of scope), so this
// cannot reopen that contract. The stamp instead lives in a root-level
// carried-forward.yaml sidecar in dst, keyed by the copy's new id,
// mirroring tombstones.yaml's own existing root-level convention.
//
// Constraints: tests root every tree at t.TempDir() (Art.7); no bare
// time.Now/rand; deterministic iteration (src's own sorted order).
// SPORT: plugins/pbd/internal/pews residue-carry-forward (ADD) — P1-E14-W3-S29-T3.
package pews

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PhaseID names one phase's tree: Root is the tree's filesystem root
// (sibling to phase.yaml/tombstones.yaml), Phase the id prefix its
// tickets declare — Store's own two fields, made concrete as the value
// type R-21.276's CarryForward(src, dst PhaseID) signature names.
type PhaseID struct {
	Root, Phase string
}

// CarriedRecord is one residue-copy's carried_from provenance stamp,
// recorded in dst's carried-forward.yaml sidecar (see this file's doc
// comment) rather than inside the copy's own ticket YAML.
type CarriedRecord struct {
	NewID       string `yaml:"new_id"`
	CarriedFrom string `yaml:"carried_from"`
}

// carriedForwardFile is the sidecar's filename at a tree's Root, sibling
// to tombstones.yaml and phase.yaml.
const carriedForwardFile = "carried-forward.yaml"

// carryItem is one residue ticket's plan: its src record, its new id in
// dst, and the rewritten copy to write there.
type carryItem struct {
	old       TicketRecord
	newID     string
	newTicket Ticket
}

// CarryForward implements R-21.276: it collects src's residue (every
// ticket not already tombstoned there — see the status caveat above),
// copies each into dst under a new id in dst's id space with a
// carried-forward.yaml provenance stamp, rewrites every depends_on entry
// naming a carried ticket to its new id while preserving every other
// entry verbatim, tombstones each original in src, and refuses with a
// typed KindConflict error — before any write — when dst is not a draft
// phase.
func CarryForward(src, dst PhaseID) error {
	dstTree, err := NewStore(dst.Root, dst.Phase).LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		return err
	}
	if !dstTree.Draft {
		return cascade.Newf(cascade.KindConflict,
			"pews: carry-forward destination phase %q is not a draft phase", dst.Phase)
	}
	srcTree, err := NewStore(src.Root, src.Phase).Load()
	if err != nil {
		return err
	}

	residue := residueTickets(srcTree)
	if len(residue) == 0 {
		return nil
	}
	items := planCarryForward(residue, dstTree, dst)
	return executeCarryForward(items, src, dst)
}

// residueTickets returns every src ticket not already recorded in
// src's own tombstones.yaml, in the tree's own sorted CanonicalID order.
func residueTickets(tree *Tree) []TicketRecord {
	tomb := map[string]bool{}
	for _, t := range tree.Tombstones {
		tomb[t.ID] = true
	}
	var out []TicketRecord
	for _, r := range tree.Tickets {
		if !tomb[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// planCarryForward assigns each residue ticket a free canonical id in
// dst (same epic/wave/sprint coordinates as in src, the smallest free
// ticket number at or above its original number), then rewrites every
// carried ticket's depends_on entries through the resulting old-id ->
// new-id map, leaving an entry naming a non-carried ticket untouched.
func planCarryForward(residue []TicketRecord, dstTree *Tree, dst PhaseID) []carryItem {
	occupied := map[string]bool{}
	for _, r := range dstTree.Tickets {
		occupied[r.CanonicalID] = true
	}
	items := make([]carryItem, 0, len(residue))
	oldToNew := map[string]string{}
	for _, rec := range residue {
		num := nextFreeTicketNum(occupied, dst.Phase, rec.EpicNum, rec.Wave, rec.Sprint, rec.TicketNum)
		newID := canonicalID(dst.Phase, rec.EpicNum, rec.Wave, rec.Sprint, num)
		occupied[newID] = true
		oldToNew[rec.Ticket.ID] = newID
		items = append(items, carryItem{old: rec, newID: newID})
	}
	for i := range items {
		t := items[i].old.Ticket
		t.ID = items[i].newID
		t.DependsOn = rewriteDeps(t.DependsOn, oldToNew)
		items[i].newTicket = t
	}
	return items
}

// nextFreeTicketNum returns the smallest ticket number at or above start
// not already occupied at (phase, epicNum, wave, sprint).
func nextFreeTicketNum(occupied map[string]bool, phase string, epicNum, wave, sprint, start int) int {
	for n := start; ; n++ {
		if !occupied[canonicalID(phase, epicNum, wave, sprint, n)] {
			return n
		}
	}
}

// rewriteDeps rewrites every entry of deps found in oldToNew to its new
// id, preserving order and leaving every other entry verbatim.
func rewriteDeps(deps []string, oldToNew map[string]string) []string {
	if deps == nil {
		return nil
	}
	out := make([]string, len(deps))
	for i, d := range deps {
		if newID, ok := oldToNew[d]; ok {
			out[i] = newID
		} else {
			out[i] = d
		}
	}
	return out
}

// executeCarryForward writes every item's copy into dst via the existing
// author.go atomicWriteTicket path (no second persistence path), not
// Create's Lint preflight: carry-forward preserves an existing contract
// as-is, and re-gating it against contract-lint policy is S-28.T4's
// concern. Stops at the first failure; earlier writes are not rolled
// back (atomicWriteTicket's per-file atomicity is the only transaction
// granularity this engine has, matching author.go's Move).
func executeCarryForward(items []carryItem, src, dst PhaseID) error {
	var carried []CarriedRecord
	for _, it := range items {
		if err := writeCarriedTicket(dst.Root, it.newID, it.newTicket); err != nil {
			return err
		}
		carried = append(carried, CarriedRecord{NewID: it.newID, CarriedFrom: it.old.Ticket.ID})
	}
	if err := appendCarriedRecords(dst.Root, carried); err != nil {
		return err
	}
	return tombstoneOriginals(src, dst, items)
}

// writeCarriedTicket resolves newID's canonical position under root and
// writes t there, refusing (KindConflict) rather than clobbering.
func writeCarriedTicket(root, newID string, t Ticket) error {
	c, err := parseCanonicalID(newID)
	if err != nil {
		return err
	}
	absPath := filepath.Join(root, relPathFor(c))
	if exists, eerr := fileExists(absPath); eerr != nil {
		return eerr
	} else if exists {
		return cascade.Newf(cascade.KindConflict, "pews: carry-forward target %q already exists", newID)
	}
	return atomicWriteTicket(absPath, t)
}

// tombstoneOriginals appends one src tombstones.yaml entry per item and
// removes each original file (validate.go's ViolationTombstoneLive
// forbids a live file at a tombstoned id).
func tombstoneOriginals(src, dst PhaseID, items []carryItem) error {
	var add []Tombstone
	for _, it := range items {
		reason := fmt.Sprintf("carried forward to %s/%s", dst.Phase, it.newID)
		add = append(add, Tombstone{ID: it.old.Ticket.ID, Reason: reason})
		absPath := filepath.Join(src.Root, it.old.RelPath)
		if rerr := os.Remove(absPath); rerr != nil {
			return cascade.Wrapf(cascade.KindInternal, rerr, "pews: removing carried-forward original %q", absPath)
		}
	}
	return appendTombstones(src.Root, add)
}

// appendTombstones reads src's existing tombstones.yaml (if any), appends
// add, and writes the result back atomically.
func appendTombstones(root string, add []Tombstone) error {
	existing, err := (&Store{Root: root}).loadTombstones()
	if err != nil {
		return err
	}
	doc := struct {
		Tombstones []Tombstone `yaml:"tombstones"`
	}{Tombstones: append(existing, add...)}
	return marshalSidecar(root, "tombstones.yaml", doc)
}

// appendCarriedRecords reads dst's existing carried-forward.yaml (if
// any), appends add, and writes the result back atomically.
func appendCarriedRecords(root string, add []CarriedRecord) error {
	existing, err := loadCarriedRecords(root)
	if err != nil {
		return err
	}
	doc := struct {
		Carried []CarriedRecord `yaml:"carried"`
	}{Carried: append(existing, add...)}
	return marshalSidecar(root, carriedForwardFile, doc)
}

// marshalSidecar YAML-encodes doc and writes it to root/name atomically.
func marshalSidecar(root, name string, doc any) error {
	data, merr := yaml.Marshal(doc)
	if merr != nil {
		return cascade.Wrapf(cascade.KindInternal, merr, "pews: encoding %s YAML", name)
	}
	return writeSidecarFile(root, name, data)
}

// loadCarriedRecords reads root/carried-forward.yaml. A missing file
// means nothing has ever been carried into this tree yet, matching
// store.go's loadTombstones convention: valid, not an error.
func loadCarriedRecords(root string) ([]CarriedRecord, error) {
	path := filepath.Join(root, carriedForwardFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pews: reading %q", path)
	}
	var doc struct {
		Carried []CarriedRecord `yaml:"carried"`
	}
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, uerr, "pews: malformed carried-forward YAML %q", path)
	}
	return doc.Carried, nil
}

// writeSidecarFile writes data to root/name atomically, reusing
// author.go's own writeAndSync helper (temp file beside the target, then
// rename).
func writeSidecarFile(root, name string, data []byte) error {
	tmp, err := os.CreateTemp(root, "."+name+"-*.tmp")
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "pews: creating temp file in %q", root)
	}
	tmpPath, path := tmp.Name(), filepath.Join(root, name)
	if werr := writeAndSync(tmp, data); werr != nil {
		_ = os.Remove(tmpPath)
		return werr
	}
	if rerr := os.Rename(tmpPath, path); rerr != nil {
		_ = os.Remove(tmpPath)
		return cascade.Wrapf(cascade.KindInternal, rerr, "pews: renaming %q to %q", tmpPath, path)
	}
	return nil
}
