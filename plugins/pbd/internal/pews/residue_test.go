// Purpose: residue.go's required unit tests — TestResidueCarryForward
// proves R-21.276's mechanics (residue selection excludes an already-
// tombstoned original, the copy lands at a new id in dst's own id space
// with a carried-forward.yaml stamp, a depends_on entry naming a carried
// ticket is rewritten while one naming a non-carried ticket is preserved
// verbatim, the original is tombstoned and removed from src, a
// collision in dst is resolved to the next free ticket number, and an
// empty residue set is a clean no-op) plus the dst-not-draft refusal
// leaving both trees byte-for-byte untouched.
// SPORT: plugins/pbd/internal/pews residue-carry-forward (ADD) — P1-E14-W3-S29-T3.
package pews

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResidueCarryForward(t *testing.T) {
	t.Run("carries an untombstoned ticket forward under a new id, stamped", testCarryForwardBasic)
	t.Run("rewrites a depends_on entry naming a carried ticket, preserves others", testCarryForwardRewritesDeps)
	t.Run("refuses when dst is not a draft phase, leaving both trees untouched", testCarryForwardRefusesNonDraftDst)
	t.Run("an empty residue set is a clean no-op", testCarryForwardEmptyResidue)
	t.Run("resolves a dst id collision to the next free ticket number", testCarryForwardCollision)
}

// newSrcDst builds a closing (non-draft) src tree root and a draft dst
// tree root, each t.TempDir()-rooted (Art.7).
func newSrcDst(t *testing.T) (src, dst PhaseID) {
	t.Helper()
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	writePhaseRecord(t, dstRoot, true)
	return PhaseID{Root: srcRoot, Phase: "P1"}, PhaseID{Root: dstRoot, Phase: "P2"}
}

func testCarryForwardBasic(t *testing.T) {
	src, dst := newSrcDst(t)
	// P1-E14-W3-S29-T1 is already done: only a tombstone entry, no live
	// ticket file (a live file at a tombstoned id is itself a structural
	// violation, validate.go's ViolationTombstoneLive), so it must never
	// count as residue.
	writeTombstoneFileForTest(t, src.Root, "P1-E14-W3-S29-T1", "done")
	mkTicketFile(t, src.Root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", nil)

	if err := CarryForward(src, dst); err != nil {
		t.Fatalf("CarryForward: %v", err)
	}

	dstTree, err := NewStore(dst.Root, "P2").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("Load(dst): %v", err)
	}
	if len(dstTree.Tickets) != 1 {
		t.Fatalf("dst Tickets = %+v, want exactly 1 carried ticket", dstTree.Tickets)
	}
	newID := dstTree.Tickets[0].Ticket.ID
	if newID == "P1-E14-W3-S29-T2" {
		t.Errorf("carried ticket kept its old id %q, want a new P2 id", newID)
	}

	carried, cerr := loadCarriedRecords(dst.Root)
	if cerr != nil {
		t.Fatalf("loadCarriedRecords: %v", cerr)
	}
	if len(carried) != 1 || carried[0].NewID != newID || carried[0].CarriedFrom != "P1-E14-W3-S29-T2" {
		t.Fatalf("carried-forward stamp = %+v, want one entry {%s, P1-E14-W3-S29-T2}", carried, newID)
	}

	srcTree, serr := NewStore(src.Root, "P1").Load()
	if serr != nil {
		t.Fatalf("Load(src): %v", serr)
	}
	if len(srcTree.Tickets) != 0 {
		t.Errorf("src Tickets = %+v, want 0 (original removed)", srcTree.Tickets)
	}
	foundTomb := false
	for _, tomb := range srcTree.Tombstones {
		if tomb.ID == "P1-E14-W3-S29-T2" {
			foundTomb = true
		}
	}
	if !foundTomb {
		t.Errorf("src Tombstones = %+v, want an entry for P1-E14-W3-S29-T2", srcTree.Tombstones)
	}
	if _, statErr := os.Stat(filepath.Join(src.Root, "epics", "E-N", "waves", "W-3",
		"sprints", "S-29", "tickets", "T-2.yaml")); !os.IsNotExist(statErr) {
		t.Errorf("original ticket file still exists on disk after carry-forward")
	}
}

func testCarryForwardRewritesDeps(t *testing.T) {
	src, dst := newSrcDst(t)
	writeTombstoneFileForTest(t, src.Root, "P1-E14-W3-S29-T1", "done") // done, not carried, no live file
	mkTicketFile(t, src.Root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", nil)
	mkTicketFile(t, src.Root, "N", 3, 29, 3, "P1-E14-W3-S29-T3",
		[]string{"P1-E14-W3-S29-T2", "P1-E14-W3-S29-T1"})

	if err := CarryForward(src, dst); err != nil {
		t.Fatalf("CarryForward: %v", err)
	}
	dstTree, err := NewStore(dst.Root, "P2").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("Load(dst): %v", err)
	}
	var t2New, t3New string
	byOld := map[string]string{}
	for _, tr := range dstTree.Tickets {
		byOld[tr.Ticket.ID] = tr.Ticket.ID
	}
	var t3Deps []string
	for _, tr := range dstTree.Tickets {
		if len(tr.Ticket.DependsOn) > 0 {
			t3New = tr.Ticket.ID
			t3Deps = tr.Ticket.DependsOn
		} else {
			t2New = tr.Ticket.ID
		}
	}
	if t3New == "" || t2New == "" {
		t.Fatalf("expected two carried tickets, got %+v", dstTree.Tickets)
	}
	if len(t3Deps) != 2 || t3Deps[0] != t2New || t3Deps[1] != "P1-E14-W3-S29-T1" {
		t.Fatalf("carried T3 depends_on = %v, want [%s, P1-E14-W3-S29-T1] (carried dep rewritten, "+
			"non-carried dep preserved verbatim)", t3Deps, t2New)
	}
}

func testCarryForwardRefusesNonDraftDst(t *testing.T) {
	src, dst := newSrcDst(t)
	writePhaseRecord(t, dst.Root, false) // overwrite: dst is NOT a draft
	mkTicketFile(t, src.Root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	srcBefore := snapshotDir(t, src.Root)
	dstBefore := snapshotDir(t, dst.Root)

	err := CarryForward(src, dst)
	if err == nil {
		t.Fatal("CarryForward: expected a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("CarryForward kind = %v (ok=%v), want KindConflict", kind, ok)
	}
	if got := snapshotDir(t, src.Root); !mapsEqual(got, srcBefore) {
		t.Errorf("src tree changed after a refused carry-forward: before=%v after=%v", srcBefore, got)
	}
	if got := snapshotDir(t, dst.Root); !mapsEqual(got, dstBefore) {
		t.Errorf("dst tree changed after a refused carry-forward: before=%v after=%v", dstBefore, got)
	}
}

func testCarryForwardEmptyResidue(t *testing.T) {
	src, dst := newSrcDst(t)
	mkTicketFile(t, src.Root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	writeTombstoneFileForTest(t, src.Root, "P1-E14-W3-S29-T1", "done")

	if err := CarryForward(src, dst); err != nil {
		t.Fatalf("CarryForward: %v", err)
	}
	dstTree, err := NewStore(dst.Root, "P2").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("Load(dst): %v", err)
	}
	if len(dstTree.Tickets) != 0 {
		t.Errorf("dst Tickets = %+v, want none carried (residue was empty)", dstTree.Tickets)
	}
}

func testCarryForwardCollision(t *testing.T) {
	src, dst := newSrcDst(t)
	mkTicketFile(t, src.Root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	// dst already occupies the coordinates src's T1 would naturally land
	// at (E-N/W-3/S-29/T-1 under P2's own id space).
	mkTicketFile(t, dst.Root, "N", 3, 29, 1, "P2-E14-W3-S29-T1", nil)

	if err := CarryForward(src, dst); err != nil {
		t.Fatalf("CarryForward: %v", err)
	}
	dstTree, err := NewStore(dst.Root, "P2").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("Load(dst): %v", err)
	}
	if len(dstTree.Tickets) != 2 {
		t.Fatalf("dst Tickets = %+v, want 2 (pre-existing + carried, at distinct ids)", dstTree.Tickets)
	}
	if dstTree.Tickets[0].CanonicalID == dstTree.Tickets[1].CanonicalID {
		t.Fatalf("collision not resolved: both tickets share canonical id %q", dstTree.Tickets[0].CanonicalID)
	}
}

// writeTombstoneFileForTest writes root/tombstones.yaml with one entry,
// so a test can mark a src ticket already-terminal before carry-forward
// runs (residue excludes it; see residue.go's status caveat).
func writeTombstoneFileForTest(t *testing.T, root, id, reason string) {
	t.Helper()
	content := "tombstones:\n- id: " + id + "\n  reason: " + reason + "\n"
	if err := os.WriteFile(filepath.Join(root, "tombstones.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tombstones.yaml: %v", err)
	}
}

// snapshotDir maps every regular file under root (relative path -> raw
// content) so a refusal test can assert byte-for-byte "untouched".
func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading %q: %v", path, rerr)
		}
		out[rel] = string(data)
		return nil
	})
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
