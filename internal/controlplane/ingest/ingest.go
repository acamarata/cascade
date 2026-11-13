// Package ingest provides plan discovery and ticket ingestion.
package ingest

import "errors"

// ErrNotImplemented is returned by stub functions that have no logic yet.
var ErrNotImplemented = errors.New("ingest: operation unavailable")

// Config holds configuration for plan discovery.
type Config struct {
	// Base is the folder searched for default roots when no explicit roots are set.
	Base string
	// Roots holds explicit root paths, for example from the owner's plan.json.
	Roots []string
	// Getenv is a function to read environment variables. Nil means no env.
	Getenv func(string) string
}

// Ingest processes the given plan roots. It is a stub that always returns
// ErrNotImplemented.
func Ingest(_ []string) error {
	return ErrNotImplemented
}
