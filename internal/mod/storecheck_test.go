package mod

import (
	"errors"
	"strings"
	"testing"
)

func TestStoreCompleteOK(t *testing.T) {
	files := []StoreFile{{Name: "runs"}}
	openers := map[string]bool{"runs": true}
	mapRows := map[string]bool{"runs": true}

	errs := CheckStoreCompleteness(files, openers, mapRows)
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d", len(errs))
	}
}

func TestStoreMissingOpener(t *testing.T) {
	files := []StoreFile{{Name: "runs"}}
	openers := map[string]bool{}
	mapRows := map[string]bool{"runs": true}

	errs := CheckStoreCompleteness(files, openers, mapRows)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !errors.Is(errs[0], ErrMissingOpener) {
		t.Errorf("expected error to wrap ErrMissingOpener, got %v", errs[0])
	}
	if !strings.Contains(errs[0].Error(), "runs") {
		t.Errorf("expected error to name 'runs', got %v", errs[0])
	}
}

func TestStoreMissingMapRow(t *testing.T) {
	files := []StoreFile{{Name: "runs"}}
	openers := map[string]bool{"runs": true}
	mapRows := map[string]bool{}

	errs := CheckStoreCompleteness(files, openers, mapRows)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !errors.Is(errs[0], ErrMissingMapRow) {
		t.Errorf("expected error to wrap ErrMissingMapRow, got %v", errs[0])
	}
	if !strings.Contains(errs[0].Error(), "runs") {
		t.Errorf("expected error to name 'runs', got %v", errs[0])
	}
}

func TestStoreMissingBoth(t *testing.T) {
	files := []StoreFile{{Name: "runs"}}
	openers := map[string]bool{}
	mapRows := map[string]bool{}

	errs := CheckStoreCompleteness(files, openers, mapRows)
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(errs))
	}

	hasOpener := false
	hasMapRow := false
	for _, err := range errs {
		if errors.Is(err, ErrMissingOpener) {
			hasOpener = true
		}
		if errors.Is(err, ErrMissingMapRow) {
			hasMapRow = true
		}
	}
	if !hasOpener || !hasMapRow {
		t.Errorf("expected errors to wrap both ErrMissingOpener and ErrMissingMapRow, got %v", errs)
	}
}
