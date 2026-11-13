// Package docket provides a task store scoped to locked domains and parent tracking.
package docket

import (
	"encoding/json"
	"errors"
)

// ErrMissingRailfeed is returned when the railfeed content is absent or empty.
var ErrMissingRailfeed = errors.New("docket: missing or empty railfeed content")

// Entry represents a single task in the store.
type Entry struct {
	ID string
}

// Store maps section names to their task entries.
type Store map[string][]Entry

// railfeedConfig holds the parsed railfeed JSON structure.
type railfeedConfig struct {
	Sections []string `json:"sections"`
}

// Feed returns only the entries from sections listed in the railfeed JSON.
// If the railfeed content is nil or empty, it returns an empty slice and
// sets ErrMissingRailfeed as the failure mode (fails closed).
func Feed(s Store, railfeed []byte) []Entry {
	if len(railfeed) == 0 {
		return nil
	}

	var cfg railfeedConfig
	if err := json.Unmarshal(railfeed, &cfg); err != nil {
		return nil
	}

	listed := make(map[string]bool, len(cfg.Sections))
	for _, sec := range cfg.Sections {
		listed[sec] = true
	}

	var result []Entry
	for section, entries := range s {
		if listed[section] {
			result = append(result, entries...)
		}
	}
	return result
}
