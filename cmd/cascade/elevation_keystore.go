// Purpose: the ONE place cmd/cascade decides which elevation keystore the
//
//	binary uses, so every call site gets the same answer.
//
// WHY: the W-3 hardening gate found that no elevated verb could succeed
//
//	from any shipped artifact — release binaries are CGO_ENABLED=0, so the
//	platform keystores are not compiled in, and enrolment failed outright.
//	elevation.SelectKeystore falls back to a file-backed device key on such
//	a host, disclosing the weaker tier rather than substituting silently.
//	Five call sites previously named elevation.NewKeystore directly; they
//	all route through here now, because a binary where one command can
//	elevate and another cannot would be worse than either answer.
//
// SPORT: cmd/cascade elevation-keystore/ADD — P1-W3-01 (W-3 gate, Art.9).
package main

import (
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
)

// productionKeystore returns this host's elevation keystore.
//
// It resolves the data directory lazily, exactly like every other lazyPaths
// consumer: the keystore is constructed at command-build time, long before
// a command runs, and a path resolved that early would be resolved against
// the wrong profile.
func productionKeystore() elevation.ElevationKeystore {
	paths := lazyPaths{}
	return elevation.SelectKeystore(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
}
