// Purpose: the production local elevation gate every daemonless elevated
//
//	CLI verb (vault get/rotate/grant, node enroll/remove/upgrade and the
//	node key verbs) consults before it touches anything.
//
// Inputs: the selected custody, the data directory holding the trust
//
//	record, the gate audit sink, the clock and the environment, all
//	injected so a test never reaches the keychain or the real profile.
//
// Outputs: nil only after a signature over a nonce this call issued was
//
//	verified by an in-process ElevationMiddleware against the trust record
//	elevation.BoundTrust bound to that custody key; otherwise a typed
//	refusal. Every attempt is audited (attempt, then grant or deny).
//
// Constraints: one custody selection per call, so the key that signs is the
//
//	key the trust was bound to. CASCADE_NO_INPUT=1 refuses before custody
//	is opened. A verb rpc.IsElevated does not report is refused (the
//	middleware would pass it through unchallenged). A missing audit sink,
//	an audit append failure, a signer error and an empty signature all
//	refuse. The challenge is issued and the attestation signed through the
//	existing primitives (issueBackupChallenge, signAttestation); this file
//	adds no second copy of either.
//
// SPORT: cmd/cascade/vault elevation gate (CHG: signed single-use proof).

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestFunc produces the attestation for a challenge. Production uses
// signAttestation; a test substitutes one to replay or misdirect an
// attestation. It only produces input: the middleware still verifies it.
type attestFunc func(ks elevation.ElevationKeystore, clock runtime.Clock, method, actionHash, nonce string) (rpc.Attestation, error)

// elevationGate is the production secrets.ElevationGate and the gate the
// node verbs call. Every dependency is resolved per call: building the
// command tree never probes the keystore or opens the store.
type elevationGate struct {
	custody func() elevation.Custody
	dataDir func() string
	sink    gateSink
	clock   runtime.Clock
	getenv  runtime.Getenv
	attest  attestFunc
}

// newElevationGate builds the gate. custody selects the signing source,
// dataDir locates the trust record and sink receives every gate row.
func newElevationGate(custody func() elevation.Custody, dataDir func() string, sink gateSink,
	clock runtime.Clock, getenv runtime.Getenv) *elevationGate {
	return &elevationGate{custody: custody, dataDir: dataDir, sink: sink, clock: clock, getenv: getenv}
}

// audit.gate_append is owner only. Declared here, the one cmd file that may
// import internal/rpc; the handler is in wire_gate_audit.go.
var _ = rpc.DeclareAccess(gateAppendMethod, rpc.AccessOwnerOnly)

// localGateParams is the challenge's params: exactly the verb and the local
// marker, so the signed action hash names the verb being authorised.
type localGateParams struct {
	Verb  string `json:"verb"`
	Local bool   `json:"local"`
}

// localGrant is what the guarded handler returns. Only a call that reached
// the handler with this gate's own witness produces it.
type localGrant struct{}

// Authorize implements secrets.ElevationGate. Every refusal is audited as
// an attempt then a deny carrying its reason (gateAuditor.refuse).
func (g *elevationGate) Authorize(ctx context.Context, verb string) error {
	params, hash, err := localChallenge(verb)
	if err != nil {
		return err
	}
	a := &gateAuditor{sink: g.sink}
	if g.getenv == nil || g.getenv("CASCADE_NO_INPUT") == "1" {
		return a.refuse(ctx, verb, hash, "no_input", cascade.Newf(cascade.KindElevationRequired,
			"elevation: %s needs local presence and CASCADE_NO_INPUT=1 (or an unreadable environment) forbids prompting for it", verb))
	}
	if g.custody == nil || g.dataDir == nil || g.clock == nil {
		return a.refuse(ctx, verb, hash, "not_configured", policy.ErrElevationRequired(verb, false, false))
	}
	custody := g.custody()
	ks, err := custody.Signer()
	if err != nil {
		return a.refuse(ctx, verb, hash, "custody_tier", err)
	}
	fingerprint, key, bindErr := g.bind(custody)
	if !policy.CanAttemptDaemonlessElevation(verb, bindErr == nil, custody.Tier().SatisfiesElevation()) {
		refusal, reason := policy.ErrElevationRequired(verb, bindErr == nil, custody.Tier().SatisfiesElevation()), "not_elevated"
		if bindErr != nil {
			refusal, reason = cascade.Wrap(cascade.KindElevationRequired, bindErr, refusal.Error()), "unbound"
		}
		return a.refuse(ctx, verb, hash, reason, refusal)
	}
	if g.sink == nil {
		return cascade.Newf(cascade.KindElevationRequired,
			"elevation: %s refused: no audit log is configured to record the attempt", verb)
	}
	return g.ceremony(ctx, verb, params, hash, custody.Tier(), ks, rpc.MapTrustStore{fingerprint: key}, a)
}

// localChallenge is the challenge's params, exactly {"verb","local"}, and
// their hash.
func localChallenge(verb string) (json.RawMessage, string, error) {
	params, err := json.Marshal(localGateParams{Verb: verb, Local: true})
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindInternal, err, "elevation: encode the local challenge")
	}
	return params, rpc.ParamsHash(params), nil
}

// bind returns the trust bound to custody's key. The trust backend is built
// here, inside BoundTrust, and nowhere else in this gate.
func (g *elevationGate) bind(custody elevation.Custody) (string, ed25519.PublicKey, error) {
	dir := g.dataDir()
	if dir == "" {
		return "", nil, cascade.New(cascade.KindUnavailable, "elevation: could not resolve the cascade data directory")
	}
	return elevation.BoundTrust(custody, elevation.NewFileBackend(dir), g.clock)
}

// preconditions reports the two facts node enrolment passes on to
// nodes.EnrollNode after Authorize has granted: is a trust record bound to
// the selected custody key, and can that custody sign for elevation. Any
// failure reports false.
func (g *elevationGate) preconditions() (enrolled, available bool) {
	if g.custody == nil || g.dataDir == nil {
		return false, false
	}
	custody := g.custody()
	_, _, err := g.bind(custody)
	return err == nil, custody.Tier().SatisfiesElevation()
}

// ceremony runs one challenge, one signature and one verification through
// a fresh ledger, so only a nonce this call issued can verify. A refusal at
// any step leaves a deny row (the middleware's own, or a.refuse's).
func (g *elevationGate) ceremony(ctx context.Context, verb string, params json.RawMessage, hash string,
	tier elevation.CustodyTier, ks elevation.ElevationKeystore, trust rpc.TrustStore, a *gateAuditor) error {
	issuer := rpc.NewWitnessIssuer()
	handler := rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger: rpc.NewNonceLedger(g.clock), Trust: trust, Clock: g.clock, Audit: a, Issuer: issuer,
		Custody: func() (string, bool) { return string(tier), tier.SatisfiesElevation() },
	})(verb, witnessGuard(issuer.Verifier(), verb, hash))
	nonce, err := issueBackupChallenge(ctx, handler, params)
	if err != nil {
		return a.refuse(ctx, verb, hash, "challenge_refused", refuseLocal(verb, err))
	}
	sign := g.attest
	if sign == nil {
		sign = signAttestation
	}
	att, err := sign(ks, g.clock, verb, hash, nonce)
	switch {
	case err != nil:
		return a.refuse(ctx, verb, hash, "sign_failed",
			cascade.Wrap(cascade.KindElevationRequired, err, "elevation: "+verb+" refused: local presence was not proved"))
	case att.SigB64 == "":
		return a.refuse(ctx, verb, hash, "no_signature",
			cascade.Newf(cascade.KindElevationRequired, "elevation: %s refused: the signer returned no signature", verb))
	}
	envelope, err := json.Marshal(backupElevatedEnvelope{Attestation: att, Args: params})
	if err != nil {
		return a.refuse(ctx, verb, hash, "encode_failed", cascade.Wrap(cascade.KindInternal, err, "elevation: encode the signed local request"))
	}
	result, err := handler(ctx, envelope)
	if err != nil {
		return a.refuse(ctx, verb, hash, refusalReason(err), refuseLocal(verb, err))
	}
	if _, ok := result.(localGrant); !ok {
		return a.refuse(ctx, verb, hash, "not_granted", cascade.Newf(cascade.KindElevationRequired, "elevation: %s was not granted", verb))
	}
	return nil
}

// witnessGuard is the handler behind the in-process middleware. It grants
// only with a witness this ceremony's issuer minted for verb over hash, so a
// call the middleware passed through unverified is still refused.
func witnessGuard(verifier rpc.WitnessVerifier, verb, hash string) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || !w.IssuedBy(verifier) || w.Method() != verb || w.ParamsHash() != hash {
			return nil, cascade.Newf(cascade.KindElevationRequired, "elevation: %s reached the gate without a verified signature", verb)
		}
		return localGrant{}, nil
	}
}

// refusalReason is a deny row's constant code for a middleware refusal: the
// error's kind (a replayed nonce is not-found), never its message.
func refusalReason(err error) string {
	if kind, ok := cascade.KindOf(err); ok {
		return kind.String()
	}
	return "denied"
}

// refuseLocal maps any middleware refusal onto the elevation-required kind,
// keeping the cause reachable.
func refuseLocal(verb string, err error) error {
	return cascade.Wrap(cascade.KindElevationRequired, err, "elevation: "+verb+" was not granted: "+err.Error())
}

// gateAuditor writes one gate call's elevation rows and remembers which
// phases it has written, so a refusal adds exactly the rows still missing.
type gateAuditor struct {
	sink      gateSink
	attempted bool
	denied    bool
}

// gateAuditKinds maps a record phase to its gate row kind.
var gateAuditKinds = map[string]string{
	rpc.ElevationPhaseAttempt: string(audit.KindElevationAttempt),
	rpc.ElevationPhaseGrant:   string(audit.KindElevationGrant),
	rpc.ElevationPhaseDeny:    string(audit.KindElevationDeny),
}

// AppendElevation implements rpc.ElevationAuditor. The gate row carries the
// verb and the params hash; a deny adds its constant reason code. Nothing
// else reaches the sink (gateRow.validate).
func (a *gateAuditor) AppendElevation(ctx context.Context, rec rpc.ElevationAuditRecord) error {
	kind, ok := gateAuditKinds[rec.Phase]
	if !ok {
		return cascade.New(cascade.KindInternal, "elevation: unknown audit phase")
	}
	if a.sink == nil {
		return cascade.New(cascade.KindUnavailable, "elevation: no audit log is configured")
	}
	_, err := a.sink.AppendGateRow(ctx, gateRow{Kind: kind, Verb: rec.Method, Reason: rec.Reason, ParamsHash: rec.ParamsHash})
	if err == nil {
		a.attempted = a.attempted || rec.Phase == rpc.ElevationPhaseAttempt
		a.denied = a.denied || rec.Phase == rpc.ElevationPhaseDeny
	}
	return err
}

// refuse records whatever this call has not yet recorded (the attempt, then
// the deny with reason) and returns cause. With no writer configured there
// is nothing to record. An append failure is returned alongside cause.
func (a *gateAuditor) refuse(ctx context.Context, verb, hash, reason string, cause error) error {
	if a.sink == nil {
		return cause
	}
	var failed []error
	if !a.attempted {
		if err := a.AppendElevation(ctx, rpc.ElevationAuditRecord{Phase: rpc.ElevationPhaseAttempt, Method: verb, ParamsHash: hash}); err != nil {
			failed = append(failed, err)
		}
	}
	if !a.denied {
		if err := a.AppendElevation(ctx, rpc.ElevationAuditRecord{Phase: rpc.ElevationPhaseDeny, Method: verb, ParamsHash: hash, Reason: reason}); err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) == 0 {
		return cause
	}
	kind, ok := cascade.KindOf(cause)
	if !ok {
		kind = cascade.KindElevationRequired
	}
	msg := cause.Error() + "; the refusal could not be audited (audit_unavailable):"
	for _, err := range failed {
		msg += " " + err.Error()
	}
	return cascade.Wrap(kind, cause, msg)
}
