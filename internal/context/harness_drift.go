package context

// Purpose: the drift half of harness reporting (P1-E16-W4-S35-T3) —
//   folding a check-only Sync result into the detected fleet, and the
//   small readers over the result.
// Constraints: split from harness.go to stay under Art.10.3's 300-line
//   cap. Drift is reported only for a DETECTED harness; see WithDrift's
//   own doc comment for why.
// SPORT: internal/context harness drift (ADD) — P1-E16-W4-S35-T3.

import "sort"

// WithDrift returns states with each detected harness's drift filled in
// from a check-only Sync result for cwd.
//
// Drift is reported ONLY for a detected harness. Cascade generates
// instruction files for all three regardless of what is installed, so a
// machine with one harness would otherwise report two files as "drifted"
// for a harness nobody has — which is true of the file and useless to the
// reader.
func WithDrift(states []HarnessState, result SyncResult) []HarnessState {
	byKind := map[HarnessKind]DriftResult{}
	for _, d := range result.Drift {
		kind := HarnessKind(d.Harness)
		// First stale entry wins: a harness with several generated files
		// is drifted if ANY of them is, and the first reason is the one
		// a reader acts on.
		if existing, seen := byKind[kind]; seen && existing.Stale {
			continue
		}
		byKind[kind] = d
	}
	out := make([]HarnessState, len(states))
	copy(out, states)
	for i := range out {
		if !out[i].Detected {
			continue
		}
		d, ok := byKind[out[i].Kind]
		if !ok {
			continue
		}
		out[i].InstructionPath = d.Path
		out[i].Drift = d.Stale
		if d.Stale {
			out[i].DriftReason = d.Reason
		}
	}
	return out
}

// DetectedKinds returns the kinds reported as installed, sorted, which is
// what a caller wanting "what is set up here" actually asks for.
func DetectedKinds(states []HarnessState) []HarnessKind {
	out := make([]HarnessKind, 0, len(states))
	for _, s := range states {
		if s.Detected {
			out = append(out, s.Kind)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
