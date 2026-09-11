// Purpose: dogfood.go's required unit tests over committed fixture trees
// in t.TempDir() (Art.7): the happy-path round-trip, idempotent
// convergence on a second Convert over an already-emitted target, and
// every error path (a lint-dirty tree, an unparseable ticket refusing the
// whole load rather than being skipped, and an unwritable dst).
// SPORT: plugins/pbd/internal/pews dogfood-convert (ADD) — P1-E14-W3-S30-T3.
package pews

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// mkCleanTree writes n lint-clean tickets (T-1..T-n) under root's
// canonical N/W3/S30 position, each depending on none of the others.
func mkCleanTree(t *testing.T, root string, n int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		id := ticketIDFor(i)
		ids = append(ids, id)
		path := ticketPath(root, "N", 3, 30, i)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(cleanTicketYAML(id)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func ticketIDFor(n int) string {
	return "P1-E14-W3-S30-T" + string(rune('0'+n))
}

func TestDogfoodConvert(t *testing.T) {
	t.Run("happy path: round trip drops nothing", testDogfoodHappyPath)
	t.Run("idempotent: second run over an emitted target is a no-op", testDogfoodIdempotent)
	t.Run("refuses a lint-dirty tree, dst untouched", testDogfoodLintDirtyRefuses)
	t.Run("refuses (never skips) an unparseable ticket", testDogfoodUnparseableRefuses)
	t.Run("refuses on an unwritable dst", testDogfoodUnwritableDst)
	t.Run("re-emits tombstones.yaml when the source carries any", testDogfoodTombstones)
}

func testDogfoodHappyPath(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	ids := mkCleanTree(t, src, 3)

	result, err := Convert(src, dst)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.TicketCount != 3 || !result.Changed || len(result.ChangedFiles) != 3 {
		t.Fatalf("result = %+v, want TicketCount 3, Changed true, 3 ChangedFiles", result)
	}

	dstTree, lerr := NewStore(dst, "P1").Load()
	if lerr != nil {
		t.Fatalf("Load(dst): %v", lerr)
	}
	if len(dstTree.Tickets) != len(ids) {
		t.Fatalf("dst has %d tickets, want %d (nothing dropped)", len(dstTree.Tickets), len(ids))
	}
	got := map[string]bool{}
	for _, r := range dstTree.Tickets {
		got[r.Ticket.ID] = true
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("dst is missing input ticket %q", id)
		}
	}
	if report, verr := Validate(dstTree); verr != nil || !report.OK() {
		t.Fatalf("Validate(dst): report=%+v err=%v, want a clean tree", report, verr)
	}
}

func testDogfoodIdempotent(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mkCleanTree(t, src, 4)

	first, err := Convert(src, dst)
	if err != nil {
		t.Fatalf("first Convert: %v", err)
	}
	if !first.Changed {
		t.Fatalf("first Convert reported Changed=false, want true (fresh dst)")
	}

	// A second Convert over the ALREADY-emitted target (dst as its own
	// src) must converge: byte-identical output, "no changes" delta.
	second, err := Convert(dst, dst)
	if err != nil {
		t.Fatalf("second Convert: %v", err)
	}
	if second.Changed || len(second.ChangedFiles) != 0 {
		t.Errorf("second Convert = %+v, want Changed=false and no ChangedFiles", second)
	}
	if second.TicketCount != first.TicketCount {
		t.Errorf("second TicketCount = %d, want %d (unchanged)", second.TicketCount, first.TicketCount)
	}
}

func testDogfoodLintDirtyRefuses(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	// minimalTicketYAML (store_test.go) has no tasks/checks/acceptance
	// criteria: structurally valid, but contract-lint-dirty.
	mkTicketFile(t, src, "N", 3, 30, 1, "P1-E14-W3-S30-T1", nil)

	if _, err := Convert(src, dst); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Convert(lint-dirty): err = %v, want KindInvalidInput", err)
	}
	entries, rerr := os.ReadDir(dst)
	if rerr != nil {
		t.Fatalf("ReadDir(dst): %v", rerr)
	}
	if len(entries) != 0 {
		t.Errorf("dst has %d entries after a refused Convert, want 0 (untouched)", len(entries))
	}
}

func testDogfoodUnparseableRefuses(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mkCleanTree(t, src, 2)
	// A third file with an invalid weight: the whole Store.Load call
	// refuses (never silently drops just this one file).
	badPath := ticketPath(src, "N", 3, 30, 3)
	if err := os.WriteFile(badPath, []byte(cleanTicketYAML("P1-E14-W3-S30-T3")+"\nweight: BOGUS\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Convert(src, dst); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Convert(unparseable ticket): err = %v, want KindInvalidInput", err)
	}
	entries, rerr := os.ReadDir(dst)
	if rerr != nil {
		t.Fatalf("ReadDir(dst): %v", rerr)
	}
	if len(entries) != 0 {
		t.Errorf("dst has %d entries after a refused Convert, want 0 (nothing partially emitted)", len(entries))
	}
}

func testDogfoodUnwritableDst(t *testing.T) {
	src := t.TempDir()
	mkCleanTree(t, src, 1)
	// A dst that is itself a regular file: MkdirAll (atomicWriteTicket's
	// own directory-creation step) fails against it.
	dstFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dstFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Convert(src, filepath.Join(dstFile, "sub")); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("Convert(unwritable dst): err = %v, want KindInternal", err)
	}
}

func testDogfoodTombstones(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mkCleanTree(t, src, 1)
	writeTombstoneFileForTest(t, src, "P1-E14-W3-S30-T9", "done")

	result, err := Convert(src, dst)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !containsString(result.ChangedFiles, "tombstones.yaml") {
		t.Errorf("ChangedFiles = %v, want tombstones.yaml", result.ChangedFiles)
	}
	data, rerr := os.ReadFile(filepath.Join(dst, "tombstones.yaml"))
	if rerr != nil || len(data) == 0 {
		t.Fatalf("dst/tombstones.yaml missing or empty: %v", rerr)
	}

	// Idempotent re-run must not rewrite an unchanged tombstones.yaml.
	second, serr := Convert(dst, dst)
	if serr != nil {
		t.Fatalf("second Convert: %v", serr)
	}
	if containsString(second.ChangedFiles, "tombstones.yaml") {
		t.Errorf("second Convert rewrote tombstones.yaml unnecessarily: %v", second.ChangedFiles)
	}
}

func containsString(list []string, s string) bool {
	return inSet(s, list)
}
