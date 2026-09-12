// Package v1 uses this file for deterministic dry-run result helpers.
// Purpose: normalize results and count mutation deltas.
// Inputs: result entries assembled after strict parsing and destination checks.
// Outputs: normalized non-nil slices and delta counts.
// Constraints: stable order comes from callers; this file never performs I/O.
// SPORT: migration/v1/ADD (P1-E26-W10-S53-T1).
package v1

// Normalize makes every result slice non-nil for stable JSON output.
func (r DryRunResult) Normalize() DryRunResult {
	if r.Changes == nil {
		r.Changes = []Change{}
	}
	if r.Journal == nil {
		r.Journal = []JournalEntry{}
	}
	if r.Reauth == nil {
		r.Reauth = []ReauthPrompt{}
	}
	return r
}

// DeltaCount reports the number of destination mutations. Existing-wins skips
// and byte-identical records are observations, not deltas.
func (r DryRunResult) DeltaCount() int {
	count := 0
	for _, change := range r.Changes {
		if change.Operation == OperationCreate || change.Operation == OperationUpdate || change.Operation == OperationTombstone {
			count++
		}
	}
	return count
}

// EmptyDelta reports whether a rerun would mutate no destination.
func (r DryRunResult) EmptyDelta() bool { return r.DeltaCount() == 0 }
