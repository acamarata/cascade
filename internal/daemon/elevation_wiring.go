// Purpose: build the one elevation middleware a process mounts on a registry:
//
//	the custody gate from the selected custody source, the trust bound to
//	that custody key by elevation.BoundTrust, one nonce ledger, and the
//	process's single redacting audit log as the auditor. Also the refuse-all
//	variant for a registry whose process holds no custody session.
//
// Inputs: the data directory, the custody selector, the audit log and the
//
//	injected clock.
//
// Outputs: an rpc.MiddlewareFunc and an ElevationStatus describing what it
//
//	enforces.
//
// Constraints: trust is bound ONCE, when the middleware is built (a new
//
//	enrolment needs a restart). A custody that cannot satisfy elevation, or
//	a record not bound to that custody's key, refuses every elevated verb
//	before a nonce is issued; a nil log leaves the auditor unset, which
//	refuses them too. Nothing here builds an audit.Log: the caller passes
//	the process's shared one.
//
// SPORT: internal/daemon NewElevationMiddleware/RefuseAllElevation (P1-SEC-03).

package daemon

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errUnknownElevationPhase refuses a record whose phase is not one of the three.
var errUnknownElevationPhase = cascade.New(cascade.KindInternal, "elevation: unknown audit phase")

// ElevationStatus describes what a built middleware enforces, for status
// surfaces and tests.
type ElevationStatus struct {
	// Tier is the selected custody tier ("platform", "presence", "file", "none").
	Tier string
	// Bound reports that an enrolment bound to the custody key was found and
	// elevated verbs can be satisfied.
	Bound bool
	// Reason says why elevated verbs are refused when they are; empty when bound.
	Reason string
	// Verifier recognises the witnesses this middleware mints, and cannot
	// mint any. The policy wiring passes it to policy.WitnessAttestor so only
	// witnesses from this daemon's gate vouch for an elevated verb; the
	// minting capability itself never leaves this package.
	Verifier rpc.WitnessVerifier
}

// NewElevationMiddleware builds the middleware for the daemon registry. sel
// picks the custody source; dataDir holds the trust record. log is the
// process's shared audit log: nil leaves elevated verbs refused.
func NewElevationMiddleware(dataDir string, sel elevation.Selector, log *audit.Log, clock runtime.Clock) (rpc.MiddlewareFunc, ElevationStatus) {
	custody := sel.Select()
	tier := custody.Tier()
	issuer := rpc.NewWitnessIssuer()
	status := ElevationStatus{Tier: string(tier), Verifier: issuer.Verifier()}
	trust := rpc.MapTrustStore{}
	deps := rpc.ElevationDeps{
		Issuer:  issuer,
		Ledger:  rpc.NewNonceLedger(clock),
		Trust:   trust,
		Clock:   clock,
		Custody: func() (string, bool) { return string(tier), tier.SatisfiesElevation() },
	}
	if log != nil {
		deps.Audit = elevationAuditor{log: log}
	}
	switch {
	case !tier.SatisfiesElevation():
		status.Reason = custody.Reason()
	default:
		fingerprint, key, err := elevation.BoundTrust(custody, elevation.NewFileBackend(dataDir), clock)
		if err != nil {
			deps.Unbound = err
			status.Reason = err.Error()
			break
		}
		trust[fingerprint] = key
		status.Bound = true
	}
	return rpc.ElevationMiddleware(deps), status
}

// RefuseAllElevation builds the middleware for a registry whose process holds
// no custody session: empty trust, a gate that never passes, tier "none".
// Every elevated verb is refused before a nonce is issued; everything else
// passes through.
func RefuseAllElevation(clock runtime.Clock) rpc.MiddlewareFunc {
	return rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger:  rpc.NewNonceLedger(clock),
		Trust:   rpc.MapTrustStore{},
		Clock:   clock,
		Custody: func() (string, bool) { return string(elevation.CustodyNone), false },
	})
}

// elevationAuditor writes elevation records to the process's audit log.
type elevationAuditor struct{ log *audit.Log }

// AppendElevation implements rpc.ElevationAuditor. The record carries the
// method and the params hash only; a deny adds its constant reason code.
func (a elevationAuditor) AppendElevation(ctx context.Context, rec rpc.ElevationAuditRecord) error {
	kind, ok := elevationKinds[rec.Phase]
	if !ok {
		return errUnknownElevationPhase
	}
	event := audit.Event{Kind: kind, Actor: "elevation", Action: rec.Method, ParamsHash: rec.ParamsHash, Outcome: rec.Phase}
	if rec.Reason != "" {
		reason, err := json.Marshal(map[string]string{"reason": rec.Reason})
		if err != nil {
			return err
		}
		event.Explain = reason
	}
	_, err := a.log.Append(ctx, event)
	return err
}

// elevationKinds maps a record phase to its audit kind.
var elevationKinds = map[string]audit.Kind{
	rpc.ElevationPhaseAttempt: audit.KindElevationAttempt,
	rpc.ElevationPhaseGrant:   audit.KindElevationGrant,
	rpc.ElevationPhaseDeny:    audit.KindElevationDeny,
}
