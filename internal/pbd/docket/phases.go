// Package docket provides phase rail feed lookups for pbd dockets.
package docket

import "errors"

// cascadeBoxMod is the only mod ID currently served by Phases.
const cascadeBoxMod = "cascade-box"

// phasesSource identifies the rail feed source for phase records.
const phasesSource = "phases"

// phasesStatusOK marks a phase feed record as successfully resolved.
const phasesStatusOK = "ok"

// ErrUnsupportedMod is returned when Phases is called with an empty or
// unknown mod ID. Phases fails closed and returns no feeds in that case.
var ErrUnsupportedMod = errors.New("docket: unsupported mod id")

// PhasesFeed is a rail feed record for a phase lookup.
type PhasesFeed struct {
	// Mod is the mod ID and also names the rail feed.
	Mod string

	// Source identifies where the feed record came from.
	Source string

	// Status reports whether the feed record resolved successfully.
	Status string
}

// Phases returns cascade-box rail feeds for D4397.
//
// The only known mod ID is "cascade-box". For that ID it returns one feed
// record with Mod "cascade-box", Source "phases", and Status "ok".
// An empty or unknown mod ID fails closed: Phases returns nil and an error.
func Phases(modID string) ([]PhasesFeed, error) {
	if modID != cascadeBoxMod {
		return nil, ErrUnsupportedMod
	}

	return []PhasesFeed{{
		Mod:    cascadeBoxMod,
		Source: phasesSource,
		Status: phasesStatusOK,
	}}, nil
}
