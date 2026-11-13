// Package ingest provides plan discovery and ticket ingestion stubs.
package ingest

import "errors"

// ErrNotImplemented is returned by stub functions that have no logic yet.
var ErrNotImplemented = errors.New("ingest: operation unavailable")

// Config holds configuration for plan discovery.
type Config struct{}

// DiscoverPlanRoots returns the root paths for plans. It is a stub that
// always returns nil and ErrNotImplemented.
func DiscoverPlanRoots(_ Config) ([]string, error) {
	return nil, ErrNotImplemented
}

// Ingest processes the given plan roots. It is a stub that always returns
// ErrNotImplemented.
func Ingest(_ []string) error {
	return ErrNotImplemented
}
