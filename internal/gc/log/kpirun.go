package log

import (
	crand "crypto/rand"
	"encoding/hex"
)

// RunID generates or retrieves a KPI run identifier.
// If override is non-empty it is returned verbatim; otherwise a fresh
// random identifier is produced on every call.
type RunID struct {
	override string
}

// NewRunID creates a RunID generator. Pass the value of the --kpi-run flag
// as override; an empty string means automatic generation.
func NewRunID(override string) *RunID {
	return &RunID{override: override}
}

// Start returns the run identifier for a new command invocation.
func (r *RunID) Start() string {
	if r.override != "" {
		return r.override
	}
	return freshID()
}

// freshID produces a random hex string suitable for use as a run id.
func freshID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}
