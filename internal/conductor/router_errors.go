// Purpose: the two Router-local sentinel errors new to S-22.T2.
//   ErrSensitivityViolation is reused from S-22.T1's errors.go and is not
//   redeclared here.
// Inputs: none.
// Outputs: none.
// Constraints: reuses the frozen 14-kind taxonomy only; never invents a
//   kind.
// SPORT: conductor.router/ADD (P1-E11-W3-S22-T2).

package conductor

import "github.com/acamarata/cascade/pkg/cascade"

// ErrNoCapableProvider is returned when FILTER 1 (capability match) leaves
// zero candidates: no lane's cached capability set satisfies every
// required capability. Selection-time capability denial performs zero
// provider calls and zero failovers (R-21.213); the single runtime
// failover on a dispatch-time capability-denied error is owned by
// execute.go.
var ErrNoCapableProvider = cascade.New(cascade.KindCapabilityDenied,
	"conductor: router: no lane satisfies the required capabilities")

// ErrAllProvidersEvicted is returned when FILTER 3 (health) leaves zero
// candidates after capability and sensitivity filtering: every remaining
// lane is either flagged for demotion or its health record in the
// snapshot is stale.
var ErrAllProvidersEvicted = cascade.New(cascade.KindUnavailable,
	"conductor: router: every capability-matched lane is evicted or unhealthy")
