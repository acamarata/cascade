package pews

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAuthorCreate(t *testing.T) {
	t.Run("authors a new ticket at its canonical position", testAuthorCreateNew)
	t.Run("refuses a non-canonical id, writing nothing", testAuthorCreateBadID)
	t.Run("refuses a phase mismatch, writing nothing", testAuthorCreatePhaseMismatch)
	t.Run("refuses to clobber an existing ticket", testAuthorCreateClobber)
	t.Run("refuses a candidate that would fail lint, writing nothing", testAuthorCreateLintFail)
	t.Run("load failure propagates (missing root)", testAuthorCreateMissingRoot)
}

func testAuthorCreateNew(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := ticketFilePath(root, "N", 3, 28, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	want, eerr := EncodeTicket(tk)
	if eerr != nil {
		t.Fatalf("EncodeTicket: %v", eerr)
	}
	if string(data) != string(want) {
		t.Errorf("written bytes differ from EncodeTicket(t):\ngot:  %q\nwant: %q", data, want)
	}
	// Round-trip fidelity: decoding and re-encoding the file on disk must
	// reproduce it byte-identically.
	decoded, derr := DecodeTicket(data)
	if derr != nil {
		t.Fatalf("DecodeTicket(written file): %v", derr)
	}
	reencoded, rerr := EncodeTicket(decoded)
	if rerr != nil {
		t.Fatalf("EncodeTicket(decoded): %v", rerr)
	}
	if string(reencoded) != string(data) {
		t.Errorf("authoring path does not round-trip byte-identically")
	}
}

func testAuthorCreateBadID(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "not-canonical")
	if err := Create(root, "P1", tk); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	assertEmpty(t, root)
}

func testAuthorCreatePhaseMismatch(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P2", tk); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	assertEmpty(t, root)
}

func testAuthorCreateClobber(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", tk); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	other := tk
	other.Title = "A different title"
	if err := Create(root, "P1", other); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("err = %v, want KindConflict", err)
	}
	// The original file must be untouched by the refused second call.
	data, _ := os.ReadFile(ticketFilePath(root, "N", 3, 28, 1))
	if want, _ := EncodeTicket(tk); string(data) != string(want) {
		t.Errorf("existing file was mutated by a refused create")
	}
}

func testAuthorCreateLintFail(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	tk.DependsOn = []string{"P1-E14-W3-S28-T99"} // dangling: fails Validate, composed by Lint
	if err := Create(root, "P1", tk); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	assertEmpty(t, root)
}

func testAuthorCreateMissingRoot(t *testing.T) {
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := Create(missing, "P1", tk); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("err = %v, want KindNotFound", err)
	}
}

func TestAuthorEdit(t *testing.T) {
	t.Run("overwrites an existing ticket", testAuthorEditOverwrite)
	t.Run("refuses when no ticket exists yet", testAuthorEditMissing)
	t.Run("refuses a replacement that would fail lint, leaving the original untouched", testAuthorEditLintFail)
}

func testAuthorEditOverwrite(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	edited := tk
	edited.Title = "An edited title"
	if err := Edit(root, "P1", edited); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	data, _ := os.ReadFile(ticketFilePath(root, "N", 3, 28, 1))
	want, _ := EncodeTicket(edited)
	if string(data) != string(want) {
		t.Errorf("written bytes differ from EncodeTicket(edited)")
	}
}

func testAuthorEditMissing(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Edit(root, "P1", tk); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("err = %v, want KindNotFound", err)
	}
	assertEmpty(t, root)
}

func testAuthorEditLintFail(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	broken := tk
	broken.Title = ""
	if err := Edit(root, "P1", broken); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	data, _ := os.ReadFile(ticketFilePath(root, "N", 3, 28, 1))
	if want, _ := EncodeTicket(tk); string(data) != string(want) {
		t.Errorf("original ticket was mutated by a refused edit")
	}
}

func TestAuthorMove(t *testing.T) {
	t.Run("relocates a ticket, updating its declared id", testAuthorMoveRelocate)
	t.Run("refuses when source and target are identical", testAuthorMoveNoop)
	t.Run("refuses when the source ticket does not exist", testAuthorMoveMissingSource)
	t.Run("refuses to clobber an existing target", testAuthorMoveClobber)
	t.Run("refuses a move that would leave a sequence gap behind", testAuthorMoveGap)
}

func testAuthorMoveRelocate(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Move(root, "P1", "P1-E14-W3-S28-T1", "P1-E14-W3-S29-T1"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	oldPath := ticketFilePath(root, "N", 3, 28, 1)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old ticket file still present: %v", err)
	}
	data, err := os.ReadFile(ticketFilePath(root, "N", 3, 29, 1))
	if err != nil {
		t.Fatalf("reading moved file: %v", err)
	}
	moved, derr := DecodeTicket(data)
	if derr != nil {
		t.Fatalf("DecodeTicket(moved): %v", derr)
	}
	if moved.ID != "P1-E14-W3-S29-T1" {
		t.Errorf("moved.ID = %q, want updated canonical id", moved.ID)
	}
}

func testAuthorMoveNoop(t *testing.T) {
	root := t.TempDir()
	if err := Move(root, "P1", "P1-E14-W3-S28-T1", "P1-E14-W3-S28-T1"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

func testAuthorMoveMissingSource(t *testing.T) {
	root := t.TempDir()
	if err := Move(root, "P1", "P1-E14-W3-S28-T1", "P1-E14-W3-S29-T1"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("err = %v, want KindNotFound", err)
	}
}

func testAuthorMoveClobber(t *testing.T) {
	root := t.TempDir()
	a := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", a); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	b := cleanTicket(t, "P1-E14-W3-S29-T1")
	if err := Create(root, "P1", b); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if err := Move(root, "P1", "P1-E14-W3-S28-T1", "P1-E14-W3-S29-T1"); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("err = %v, want KindConflict", err)
	}
	// Neither file may have been touched by the refused move.
	if _, err := os.Stat(ticketFilePath(root, "N", 3, 28, 1)); err != nil {
		t.Errorf("source file removed by a refused move: %v", err)
	}
}

func testAuthorMoveGap(t *testing.T) {
	root := t.TempDir()
	t1 := cleanTicket(t, "P1-E14-W3-S28-T1")
	if err := Create(root, "P1", t1); err != nil {
		t.Fatalf("Create t1: %v", err)
	}
	t2 := cleanTicket(t, "P1-E14-W3-S28-T2")
	if err := Create(root, "P1", t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}
	// Moving T1 away leaves only T2 in the sprint: number 1 missing, a
	// ViolationGap Validate (composed by Lint) must refuse.
	if err := Move(root, "P1", "P1-E14-W3-S28-T1", "P1-E14-W3-S30-T1"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	if _, err := os.Stat(ticketFilePath(root, "N", 3, 28, 1)); err != nil {
		t.Errorf("source file removed by a refused move: %v", err)
	}
	if _, err := os.Stat(ticketFilePath(root, "N", 3, 30, 1)); !os.IsNotExist(err) {
		t.Errorf("target file created by a refused move")
	}
}

func TestAuthorAtomicWriteTicket(t *testing.T) {
	root := t.TempDir()
	tk := cleanTicket(t, "P1-E14-W3-S28-T1")
	path := ticketFilePath(root, "N", 3, 28, 1)
	if err := atomicWriteTicket(path, tk); err != nil {
		t.Fatalf("atomicWriteTicket: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "T-1.yaml" {
		t.Errorf("directory contents = %v, want exactly [T-1.yaml] (no leftover temp file)", entries)
	}
}

// ticketFilePath mirrors relPathFor for tests that need an absolute path
// without constructing an idComponents value.
func ticketFilePath(root, epicLetters string, wave, sprint, ticket int) string {
	return filepath.Join(root, "epics", "E-"+epicLetters, "waves",
		fmt.Sprintf("W-%d", wave), "sprints", fmt.Sprintf("S-%02d", sprint),
		"tickets", fmt.Sprintf("T-%d.yaml", ticket))
}

// assertEmpty fails the test if root's epics tree carries any ticket file
// at all, proving a refused Create/Edit wrote nothing.
func assertEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "epics"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("epics dir not empty after a refused write: %v", entries)
	}
}
