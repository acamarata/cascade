package memory

// Purpose: the consolidation record codec's refusals. The account of
//   records already removed is the last thing in this package that may be
//   silently replaced, so every way of failing to read one is refused
//   distinctly rather than folded into "absent". Split from
//   consolidation_errors_test.go under the 300-line file cap.
// Constraints: pure codec tests; no store, no clock, no I/O beyond the
//   fixture's own temp directory.

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

// TestConsolidationUnknownAccountVersionIsRefused proves the
// forward-compatibility refusal is distinct from a damaged file.
func TestConsolidationUnknownAccountVersionIsRefused(t *testing.T) {
	_, err := decodeConsolidation([]byte(`{"format":99,"consolidated_id":"project/a"}`))
	if !errors.Is(err, ErrUnsupportedConsolidationFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedConsolidationFormat", err)
	}
}

// TestConsolidationAccountWithoutSurvivorIsRefused proves a record that
// names nothing is refused rather than read as an empty account.
func TestConsolidationAccountWithoutSurvivorIsRefused(t *testing.T) {
	_, err := decodeConsolidation([]byte(`{"format":1}`))
	if !errors.Is(err, ErrMalformedConsolidation) {
		t.Fatalf("err = %v, want ErrMalformedConsolidation", err)
	}
}

// TestConsolidationMissingAccountIsNotAnError proves a missing file reads
// as "no account yet" rather than a fault.
func TestConsolidationMissingAccountIsNotAnError(t *testing.T) {
	f := newConsolidationFixture(t)
	_, found, err := f.c.loadRecord(filepath.Join(f.base, "nothing-here.json"))
	if err != nil || found {
		t.Fatalf("loadRecord on a missing file = (found=%v, %v), want (false, nil)", found, err)
	}
	if !isNotExist(fs.ErrNotExist) {
		t.Error("isNotExist does not recognize fs.ErrNotExist")
	}
}
