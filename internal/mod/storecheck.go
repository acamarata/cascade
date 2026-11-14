package mod

import (
	"errors"
	"fmt"
)

// StoreFile represents a file in a store.
type StoreFile struct {
	// Name is the identifier of the store file.
	Name string
}

// ErrMissingOpener indicates that a store file lacks an opener function.
var ErrMissingOpener = errors.New("missing opener")

// ErrMissingMapRow indicates that a store file lacks an entity-map row.
var ErrMissingMapRow = errors.New("missing map row")

// CheckStoreCompleteness verifies that every store file has both an opener
// function and an entity-map row. It returns a slice of errors for each
// missing component. An empty result means the store is complete.
func CheckStoreCompleteness(files []StoreFile, openers, mapRows map[string]bool) []error {
	var errs []error
	for _, f := range files {
		if !openers[f.Name] {
			errs = append(errs, fmt.Errorf("file %q: %w", f.Name, ErrMissingOpener))
		}
		if !mapRows[f.Name] {
			errs = append(errs, fmt.Errorf("file %q: %w", f.Name, ErrMissingMapRow))
		}
	}
	return errs
}
