package browser

import (
	"testing"
)

// TestNotesViewChainOrder verifies that NotesView preserves chain order,
// duplicates, and empty paths, and that a nil or empty chain yields nil
// without calling the opener.
func TestNotesViewChainOrder(t *testing.T) {
	chain := []ChainEntry{
		{Name: "GCI", Path: "/gci"},
		{Name: "APC", Path: "/apc"},
		{Name: "PPC", Path: "/ppc"},
	}

	called := false
	opener := &testOpener{fn: func(_ string) error {
		called = true
		return nil
	}}

	rows := NotesView(chain, opener)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	expected := []Row{
		{Name: "GCI", Path: "/gci"},
		{Name: "APC", Path: "/apc"},
		{Name: "PPC", Path: "/ppc"},
	}
	for i, want := range expected {
		if rows[i] != want {
			t.Errorf("row %d: got %+v, want %+v", i, rows[i], want)
		}
	}

	if called {
		t.Error("opener should not be called by NotesView")
	}

	// Duplicate entries are preserved.
	dupChain := []ChainEntry{
		{Name: "GCI", Path: "/gci"},
		{Name: "GCI", Path: "/gci"},
	}
	dupRows := NotesView(dupChain, opener)
	if len(dupRows) != 2 {
		t.Fatalf("expected 2 rows for duplicate chain, got %d", len(dupRows))
	}
	if dupRows[0] != dupRows[1] {
		t.Errorf("duplicate rows differ: %+v vs %+v", dupRows[0], dupRows[1])
	}

	// Empty paths are preserved.
	emptyPathChain := []ChainEntry{{Name: "X", Path: ""}}
	epRows := NotesView(emptyPathChain, opener)
	if len(epRows) != 1 || epRows[0].Path != "" {
		t.Errorf("expected one row with empty path, got %+v", epRows)
	}

	// Nil chain returns nil.
	if got := NotesView(nil, opener); got != nil {
		t.Errorf("expected nil for nil chain, got %v", got)
	}

	// Empty chain returns nil.
	if got := NotesView([]ChainEntry{}, opener); got != nil {
		t.Errorf("expected nil for empty chain, got %v", got)
	}

	// Nil opener is accepted without panic.
	_ = NotesView(chain, nil)
}

// testOpener implements Opener for testing purposes.
type testOpener struct {
	fn func(path string) error
}

// Open delegates to the injected function.
func (o *testOpener) Open(path string) error {
	if o.fn != nil {
		return o.fn(path)
	}
	return nil
}
