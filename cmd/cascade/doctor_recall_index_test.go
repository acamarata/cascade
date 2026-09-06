// Purpose: proves the retrieval_index doctor check is mounted on the
// REAL productionCheckRegistry (not a test-built registry), so a check
// that exists, compiles, and passes its own package tests but is never
// registered here reads as absent from `cascade doctor` — the exact
// pattern doctor.go's own comment names as the reason only checks with a
// real data source are mounted.
//
// SPORT: cmd/cascade/doctor (CHANGED, retrieval_index check, P1-E06-W2-S11-T4).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
)

// TestDoctorRetrievalIndexCheckIsRegistered proves productionCheckRegistry
// (the exact function newDoctorCmd's production path calls) mounts the
// retrieval_index check. Deleting the reg.Register call for it from
// doctor.go turns this red.
func TestDoctorRetrievalIndexCheckIsRegistered(t *testing.T) {
	reg := productionCheckRegistry()
	for _, check := range reg.List() {
		if check.Name() == lifecycle.DoctorCheckName {
			return
		}
	}
	t.Fatalf("retrieval_index is not registered on the real productionCheckRegistry")
}
