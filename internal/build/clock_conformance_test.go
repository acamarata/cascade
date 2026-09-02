// Purpose: pin the two structurally identical Clock interfaces together.
// Inputs:   the internal/runtime and internal/testkit clock declarations.
// Outputs:  a compile error the moment the two contracts diverge.
// Constraints: assertion-only — this file must contain no runtime logic.
//
//	internal/testkit deliberately declares its own Clock rather than importing
//	internal/runtime, because internal/runtime's tests are in-package and the
//	import would cycle (R-14.126). Structural typing makes the twin work, but
//	nothing would otherwise catch the two drifting apart, so the conformance is
//	asserted here, in a package free to import both.
//
// SPORT: internal/build — clock-conformance gate.
package build

import (
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
)

// Every testkit clock must satisfy the runtime Clock contract. Adding a method
// to internal/runtime.Clock without adding it to internal/testkit.Clock (or
// vice versa) breaks this line at compile time.
var (
	_ runtime.Clock = testkit.RealClock{}
	_ runtime.Clock = (*testkit.FrozenClock)(nil)

	_ testkit.Clock = runtime.SystemClock{}
)
