package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
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
	// Audit records every elevated dispatch (attempt, then grant or deny).
	// Nil means nothing can be recorded, so every elevated verb refuses.
	Audit ElevationAuditor
	// Unbound, when set, refuses every elevated verb with this error before
	// a nonce is issued or a record is written: the custody tier is fine but
	// no enrolment is bound to it, so no attestation could ever verify.
	Unbound error
	// Issuer is the capability to mint witnesses. Nil means the gate still
	// verifies and audits, but the handler runs with no witness: only the
	// middleware that guards the trust domain an attestor reads carries one.
	Issuer *WitnessIssuer
}

// envelope keys, matched case-insensitively like the handlers' own decoding.
const (
	envelopeAttestationKey = "_attestation"
	envelopeArgsKey        = "_args"
)

// ElevationMiddleware builds the elevation MiddlewareFunc.
//
// A non-elevated call passes through untouched. An elevated call is first
// held to the custody gate, the platform check, the unbound check and the
// audit seam; then, without an attestation, it gets ELEVATION_REQUIRED plus a
// nonce; with one it is verified and, only after the grant record was
// appended, runs with a VerifiedElevation witness in its context and the
// original ("_args") params.
//
// Every dispatch through it reads the elevation table, which seals the table,
// so the first dispatch closes registration.
//
// Envelope rule: for a verb that is in the table at all, params carrying an
// `_attestation` or `_args` key are ALWAYS verified, whatever the verb's
// conditional rule says, and the handler receives `_args` only. A conditional
// rule is consulted for envelope-free params alone, so a loosening request
// cannot hide behind an envelope that makes the rule read "narrowing".
//
// Order per elevated dispatch: custody gate -> platform -> unbound -> audit
// present -> (challenge: nonce, attempt record) | (attested: attempt record,
// VerifyAttestation, grant or deny record) -> handler. The attempt record is
// written only when a nonce is issued or an attestation is about to be
// verified, so the nonce ledger's bound is also the bound on anonymous
// appends. Any append error refuses; the handler never runs after a failed
// grant append. platformElevationRefusal supplies the Windows override.
func ElevationMiddleware(d ElevationDeps) MiddlewareFunc {
	return func(method string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			ctx = withoutVerifiedElevation(ctx)
			env, enveloped, err := parseEnvelope(params)
			if err != nil {
				if IsElevatedVerb(method) {
					return nil, err
				}
				return next(ctx, params)
			}
			if !needsElevation(method, params, enveloped) {
				return next(ctx, params)
			}
			if refusal := d.preflight(); refusal != nil {
				return nil, refusal
			}
			if env.Attestation == nil {
				return nil, d.require(ctx, method, params)
			}
			return d.attest(ctx, *env.Attestation, method, env.Args, next)
		}
	}
}

// needsElevation applies the envelope rule.
func needsElevation(method string, params json.RawMessage, enveloped bool) bool {
	if enveloped && IsElevatedVerb(method) {
		return true
	}
	return IsElevated(method, params)
}

// parseEnvelope reads the elevation envelope out of params. A params value
// that is not a JSON object, or whose keys collide once case is folded, is
// not an envelope. An `_attestation` that is present but not an attestation
// object is an error: it is neither a challenge request nor a valid answer.
func parseEnvelope(params json.RawMessage) (elevatedEnvelope, bool, error) {
	var env elevatedEnvelope
	fields, bad := unparseableParams(params)
	if bad {
		return env, false, nil
	}
	rawAtt, hasAtt := fields[envelopeAttestationKey]
	rawArgs, hasArgs := fields[envelopeArgsKey]
	if !hasAtt && !hasArgs {
		return env, false, nil
	}
	env.Args = rawArgs
	if hasAtt && strings.TrimSpace(string(rawAtt)) != "null" {
		var att Attestation
		if err := json.Unmarshal(rawAtt, &att); err != nil {
			return env, true, cascade.Wrap(cascade.KindInvalidInput, err, "elevation: _attestation is not an attestation object")
		}
		env.Attestation = &att
	}
	return env, true, nil
}

// preflight runs the checks that need no state: custody, platform, binding
// and the audit seam. A refusal here writes nothing and issues no nonce.
func (d ElevationDeps) preflight() *ErrorObject {
	tier, ok := "none", false
	if d.Custody != nil {
		tier, ok = d.Custody()
	}
	if !ok {
		return custodyRefusal(tier)
	}
	if refusal := platformElevationRefusal(); refusal != nil {
		return refusal
	}
	if d.Unbound != nil {
		return errorObjectFrom(d.Unbound)
	}
	if d.Audit == nil || d.Ledger == nil || d.Trust == nil {
		return errorObjectFrom(cascade.New(cascade.KindUnavailable, "elevation: not configured (no audit, ledger or trust store); elevated verbs are refused"))
	}
	return nil
}

// require issues a fresh nonce bound to {method, paramsHash}, records the
// attempt, and returns the ELEVATION_REQUIRED error carrying the nonce. When
// the attempt cannot be recorded the nonce is withdrawn and the call refused.
func (d ElevationDeps) require(ctx context.Context, method string, params json.RawMessage) *ErrorObject {
	hash := hashParams(params)
	nonce, err := d.Ledger.Issue(method, hash)
	if err != nil {
		return errorObjectFrom(err)
	}
	if err := d.Audit.AppendElevation(ctx, ElevationAuditRecord{Phase: ElevationPhaseAttempt, Method: method, ParamsHash: hash}); err != nil {
		d.Ledger.revoke(nonce)
		return auditRefusal(err)
	}
	return &ErrorObject{
		Code:    codeElevationRequired,
		Message: "elevation required: " + method,
		Data:    elevationRequiredData{Reason: "ELEVATION_REQUIRED", Nonce: nonce},
	}
}

// attest verifies att against the original ("_args") params and, only when
// the grant record was appended, dispatches to next with those params and a
// witness; on a verification failure it records the deny and returns the
// typed denial as-is.
func (d ElevationDeps) attest(ctx context.Context, att Attestation, method string, args json.RawMessage, next HandlerFunc) (any, error) {
	hash := hashParams(args)
	// Claim the nonce before any record is written: one that was never
	// issued, was replayed, reaped or burnt, or is being verified by a
	// concurrent call, is refused without a record, so anonymous attested
	// calls cannot grow the audit log.
	if err := d.Ledger.claim(att.Nonce); err != nil {
		return nil, err
	}
	if err := d.Audit.AppendElevation(ctx, ElevationAuditRecord{Phase: ElevationPhaseAttempt, Method: method, ParamsHash: hash}); err != nil {
		d.Ledger.release(att.Nonce)
		return nil, auditRefusal(err)
	}
	now := elevationNow(d.Clock)
	if err := VerifyAttestation(att, d.Trust, d.Ledger, method, hash, now); err != nil {
		// A pending nonce is single use even when verification fails: it is
		// burnt, not freed, so one challenge buys one verification record
		// and keeps its ledger slot until it expires.
		d.Ledger.burn(att.Nonce)
		// The denial is returned whether or not its record could be
		// appended: a failed deny append loses a record, never a refusal.
		_ = d.Audit.AppendElevation(ctx, ElevationAuditRecord{Phase: ElevationPhaseDeny, Method: method, ParamsHash: hash, Reason: denyReason(err)})
		return nil, err
	}
	if err := d.Audit.AppendElevation(ctx, ElevationAuditRecord{Phase: ElevationPhaseGrant, Method: method, ParamsHash: hash}); err != nil {
		return nil, auditRefusal(err)
	}
	if d.Issuer == nil {
		return next(ctx, args)
	}
	return next(withVerifiedElevation(ctx, newVerifiedElevation(d.Issuer, method, hash, now)), args)
}

// denyReason is the short constant code a deny record carries: the error's
// kind, never its message (a message can echo attacker-chosen text).
func denyReason(err error) string {
	if kind, ok := cascade.KindOf(err); ok {
		return kind.String()
	}
	return "denied"
}

// auditRefusal is the refusal for an elevation step that could not be
// recorded.
func auditRefusal(err error) *ErrorObject {
	return errorObjectFrom(cascade.Wrap(cascade.KindUnavailable, err,
		"elevation: the audit record could not be written; the call was refused"))
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
