package rpc

import (
	"context"
	"encoding/json"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"time"
)

// ReasonCustodyTier identifies a terminal custody refusal on the wire.
const ReasonCustodyTier = "ELEVATION_CUSTODY_TIER"

// CustodyGate reports source authority and whether it satisfies elevation.
type CustodyGate func() (tier string, ok bool)

// ElevationDeps supplies the middleware's custody and attestation dependencies.
type ElevationDeps struct {
	Ledger  *NonceLedger
	Trust   TrustStore
	Clock   runtime.Clock
	Custody CustodyGate
}

// ElevationMiddleware builds the elevation MiddlewareFunc: for a
// non-elevated method it is a pure pass-through; for an elevated method
// without a satisfying attestation it returns ELEVATION_REQUIRED + nonce;
// with an attestation attached (elevatedEnvelope._attestation) it verifies
// and, on success, calls next with the original ("_args") params.
// platformElevationRefusal (elevation_unix.go / elevation_windows.go)
// supplies the tier-2 Windows override.
func ElevationMiddleware(d ElevationDeps) MiddlewareFunc {
	ledger, trust, clock := d.Ledger, d.Trust, d.Clock
	return func(method string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			var env elevatedEnvelope
			_ = json.Unmarshal(params, &env)

			checkParams := params
			if env.Attestation != nil {
				checkParams = env.Args
			}
			if !IsElevated(method, checkParams) {
				return next(ctx, params)
			}
			tier, ok := "none", false
			if d.Custody != nil {
				tier, ok = d.Custody()
			}
			if !ok {
				return nil, custodyRefusal(tier)
			}
			if refusal := platformElevationRefusal(); refusal != nil {
				return nil, refusal
			}
			if env.Attestation == nil {
				return nil, requireElevation(ledger, method, params)
			}
			return attestAndProceed(ctx, *env.Attestation, ledger, trust, clock, method, env.Args, next)
		}
	}
}

// requireElevation issues a fresh nonce bound to {method, paramsHash} and
// returns the ELEVATION_REQUIRED error carrying it.
func requireElevation(ledger *NonceLedger, method string, params json.RawMessage) *ErrorObject {
	hash := hashParams(params)
	nonce, err := ledger.Issue(method, hash)
	if err != nil {
		return errorObjectFrom(err)
	}
	return &ErrorObject{
		Code:    codeElevationRequired,
		Message: "elevation required: " + method,
		Data:    elevationRequiredData{Reason: "ELEVATION_REQUIRED", Nonce: nonce},
	}
}

// attestAndProceed verifies att against the original ("_args") params and,
// on success, dispatches to next with those original params; on failure it
// returns the typed denial as-is.
func attestAndProceed(ctx context.Context, att Attestation, ledger *NonceLedger, trust TrustStore,
	clock runtime.Clock, method string, args json.RawMessage, next HandlerFunc) (any, error) {
	hash := hashParams(args)
	now := elevationNow(clock)
	if err := VerifyAttestation(att, trust, ledger, method, hash, now); err != nil {
		return nil, err
	}
	return next(ctx, args)
}

// elevationNow reads the injected clock, never the wall clock directly
// (R-14.132's forbidigo + AST alias gate).
func elevationNow(clock runtime.Clock) time.Time {
	return clock.Now()
}

// custodyRefusal maps the same typed refusal used by local callers onto the wire.
func custodyRefusal(tier string) *ErrorObject {
	err := elevation.ErrCustodyTier(elevation.CustodyTier(tier), "custody gate refused elevation")
	refusal := errorObjectFrom(err)
	selected, _ := elevation.CustodyTierOf(err)
	refusal.Data = map[string]string{"reason": ReasonCustodyTier, "tier": string(selected)}
	return refusal
}
