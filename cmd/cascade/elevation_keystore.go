// Purpose: compose production custody lazily for local elevated operations.
// Inputs: active profile paths. Outputs: source-classified custody.
// Constraints: command construction never probes a keystore. SPORT: elevation composition.
package main

import (
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
)

// productionCustody resolves custody in the active profile.
func productionCustody() elevation.Custody {
	paths := lazyPaths{}
	return elevation.SelectCustody(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
}
