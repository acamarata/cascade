// Package ingest provides plan discovery and ticket ingestion.
package ingest

// Config holds configuration for plan discovery.
type Config struct {
	// Base is the folder searched for default roots when no explicit roots are set.
	Base string
	// Roots holds explicit root paths, for example from the owner's plan.json.
	Roots []string
	// Getenv is a function to read environment variables. Nil means no env.
	Getenv func(string) string
}
