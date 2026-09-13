package rpc

// Purpose (this file): FuzzSupervisorSnapshotParams — proves
// decodeSupervisorSnapshotParams, supervisor.snapshot's real JSON param
// decoder, never panics on adversarial input (06-FORGE-SPEC §5.7 rule 7:
// "FuzzXxx required for JSON param decoder").
// Seed corpus: testdata/fuzz/FuzzSupervisorSnapshotParams/seed001 (per
// R-21.266, this package-local path, never internal/testdata/fuzz/).

import "testing"

func FuzzSupervisorSnapshotParams(f *testing.F) {
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"unexpected":1}`))
	f.Add([]byte(`{`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`"a string, not an object"`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_ = decodeSupervisorSnapshotParams(data) // must never panic; an error is fine
	})
}
