// Purpose: the node agent's RPC registry (node.enroll, node.heartbeat and
//
//	the dispatch leg) behind the elevation middleware, split out of
//	serve.go under the 300-line file cap.
//
// Inputs: a ServeDeps from the CLI composition root.
// Outputs: BuildServeRegistry.
// Constraints: the registry always carries an elevation middleware. Node
//
//	serve is a separate process with no custody session, so by default the
//	middleware refuses every elevated verb before a nonce is issued; it
//	imports no daemon package.
//
// SPORT: internal/nodes BuildServeRegistry (P1-E17-W4-S36-T2; elevation P1-SEC-03).

package nodes

import (
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
)

// serveClock adapts the package Clock to the elevation middleware's clock;
// a nil Clock reads as the zero time, which only makes nonces look expired.
type serveClock struct{ c Clock }

// Now implements runtime.Clock.
func (s serveClock) Now() time.Time {
	if s.c == nil {
		return time.Time{}
	}
	return s.c.Now()
}

// refuseAllElevation builds the middleware for a registry whose process holds
// no custody session: empty trust, a gate that never passes, tier "none".
func refuseAllElevation(clock Clock) rpc.MiddlewareFunc {
	c := serveClock{c: clock}
	return rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger:  rpc.NewNonceLedger(c),
		Trust:   rpc.MapTrustStore{},
		Clock:   c,
		Custody: func() (string, bool) { return string(elevation.CustodyNone), false },
	})
}

// BuildServeRegistry mounts node.enroll (S-36.T1's RegisterHandlers) and
// node.heartbeat (this ticket's RegisterHeartbeatHandler) on a fresh
// *rpc.Registry — the node agent's complete RPC surface as of this
// ticket. internal/nodes is an internal/rpc SERVER package (it is allowed
// to name rpc.Registry); the CLI composition root (cmd/cascade/node_serve.go)
// is a client of THIS package instead, through NewServeRegistry below,
// because cmd/cascade may never import internal/rpc directly (the
// cmd-rpc-server boundary, internal/client/boundary_test.go). serve_test.go
// drives Dispatch through the identical registry a real client would use,
// and proves the wiring is load-bearing by showing Dispatch fails with
// method-not-found when this function is never called.
//
// The registry carries the elevation middleware from deps.Elevation, or the
// refuse-all one when that is nil (see ServeDeps.Elevation); it is never
// returned without one, so node.enroll cannot run unattested over this
// surface.
func BuildServeRegistry(deps ServeDeps) *rpc.Registry {
	registry := rpc.NewRegistry()
	mw := deps.Elevation
	if mw == nil {
		mw = refuseAllElevation(deps.Clock)
	}
	registry.Use(mw)
	RegisterHandlers(registry, EnrollDeps{
		Records:             deps.Records,
		KnownHosts:          deps.KnownHosts,
		Keystore:            deps.Keystore,
		ControllerNodeID:    deps.Self.NodeID,
		ControllerPubKeyB64: deps.Self.PubKeyB64(),
	}, deps.Precondition)
	RegisterHeartbeatHandler(registry, HeartbeatDeps{
		Records:   deps.Records,
		Sequences: deps.Sequences,
		Clock:     deps.Clock,
		Timeout:   deps.Timeout,
	})
	// The node-side dispatch leg (S-37.T2). Mounted only when the
	// composition root supplied a durable action log: without one there is
	// no dedup, and a node that answers a redelivered action twice is
	// worse than one that does not answer at all.
	if deps.Actions != nil {
		RegisterExecuteHandler(registry, ExecuteDeps{
			Actions:      deps.Actions,
			Sign:         deps.Keystore.Sign,
			Self:         deps.Self,
			EnrollmentID: deps.EnrollmentID,
			Sequences:    deps.Sequences,
		})
	}
	return registry
}
