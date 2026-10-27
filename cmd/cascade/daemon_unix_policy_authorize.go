//go:build !windows

// Purpose: the policy.authorize handler, the ONLY production writer of the
// same-turn ledger. An operator's attested call ends the previous turn and
// records one single-use, two-minute authorization of one exact action for
// the MCP subject (or the calling principal, when the call carries one).
// Inputs: params {"action": "<exact action text>"} and the verified elevation
// witness the middleware attached (never a request field, never chat text).
// Outputs: {"nonce", "expires_at"}; one audit row per outcome.
// Constraints: order is a) witness for policy.authorize over these exact
// params, else KindElevationRequired with the ledger untouched; b) EndTurn;
// c) refuse an action the policy denies unconditionally, then
// AuthorizeWithExpiry, which refuses empty, elevation-class and duplicate
// actions; d) audit the grant, and on an audit error Consume the entry and
// return the error. A refusal whose audit fails is still a refusal, and the
// audit failure is wrapped into it rather than dropped. The grant's audit row
// carries its nonce in Explain, so two grants of one action stay apart.
// SPORT: cmd/cascade policy.authorize handler (P1-SEC-10).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The outcome codes policy.authorize audits. A ledger refusal is audited
// under the ledger's own stable code (sameturn-duplicate,
// sameturn-elevation-required).
const (
	sameTurnGranted       = "sameturn-granted"
	sameTurnUnattested    = "sameturn-unattested"
	sameTurnInvalid       = "sameturn-invalid"
	sameTurnUnconditional = "sameturn-unconditional-deny"
	sameTurnRefused       = "sameturn-refused"
)

// policy.authorize is owner-only. The declaration lives here, in the one
// composition-root file allowed to import internal/rpc for this verb.
var _ = rpc.DeclareAccess(policyAuthorizeMethod, rpc.AccessOwnerOnly)

// mountPolicyAuthorize attaches parts.SameTurn to parts.Engine as its layer 1
// same-turn authorizer and registers policy.authorize over that ledger,
// auditing every outcome through log.
func mountPolicyAuthorize(reg *rpc.Registry, parts policyEngineParts, log audit.Writer) {
	parts.Engine.WithSameTurnAuthorizer(parts.SameTurn)
	reg.Register(policyAuthorizeMethod, newPolicyAuthorizer(parts.SameTurn, log).handle)
}

// principalSubjectPrefix keeps a principal's subject ID apart from the shared
// MCP subject's, so no principal can be minted with an ID that collides.
const principalSubjectPrefix = "principal:"

// mcpCallSubject is the subject a same-turn grant binds to and an MCP
// evaluation is made as: the calling principal when the call carries one
// (P1-SEC-18), else the shared mcpToolSubject. Binding to the principal keeps
// one MCP session from spending another's authorization.
func mcpCallSubject(ctx context.Context) policy.Subject {
	if c, ok := principal.CallerFrom(ctx); ok && c.Principal != nil && c.Principal.ID != "" {
		return policy.Subject{Kind: mcpToolSubject.Kind, ID: principalSubjectPrefix + c.Principal.ID}
	}
	return mcpToolSubject
}

// policyAuthorizer serves policy.authorize over the process's one ledger.
type policyAuthorizer struct {
	ledger     *policy.SameTurnLedger
	audit      audit.Writer
	classifier policy.CommandClassifier
}

// newPolicyAuthorizer builds the handler over ledger, auditing through log.
func newPolicyAuthorizer(ledger *policy.SameTurnLedger, log audit.Writer) *policyAuthorizer {
	return &policyAuthorizer{ledger: ledger, audit: log, classifier: policy.NewCommandClassifier()}
}

// handle is the policy.authorize handler.
func (a *policyAuthorizer) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	subject := mcpCallSubject(ctx)
	witness, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || witness.Method() != policyAuthorizeMethod || witness.ParamsHash() != rpc.ParamsHash(raw) {
		return nil, a.refuse(ctx, subject, raw, sameTurnUnattested, cascade.New(cascade.KindElevationRequired,
			"policy.authorize needs a verified elevation for this call"))
	}
	a.ledger.EndTurn()
	var params policyAuthorizeParams
	if err := decodeAuthorizeParams(raw, &params); err != nil {
		return nil, a.refuse(ctx, subject, raw, sameTurnInvalid, err)
	}
	if err := a.refuseUnconditional(ctx, params.Action); err != nil {
		return nil, a.refuse(ctx, subject, raw, sameTurnUnconditional, err)
	}
	nonce, expiresAt, err := a.ledger.AuthorizeWithExpiry(ctx, subject, params.Action)
	if err != nil {
		return nil, a.refuse(ctx, subject, raw, refusalCode(err), err)
	}
	if err := a.record(ctx, subject, raw, sameTurnGranted, nonce); err != nil {
		a.ledger.Consume(subject, params.Action)
		return nil, cascade.Wrap(cascade.KindUnavailable, err,
			"policy.authorize: the grant could not be audited, so it was withdrawn")
	}
	return policyAuthorizeResult{Nonce: nonce, ExpiresAt: expiresAt}, nil
}

// refuse audits a refusal under outcome and returns refusal. When the audit
// append fails the call is still refused: the reply keeps the refusal's kind
// and message and wraps the append error, so the failure reaches the caller
// instead of being dropped.
func (a *policyAuthorizer) refuse(
	ctx context.Context, subject policy.Subject, raw json.RawMessage, outcome string, refusal error,
) error {
	if err := a.record(ctx, subject, raw, outcome, ""); err != nil {
		kind, ok := cascade.KindOf(refusal)
		if !ok {
			kind = cascade.KindPermissionDenied
		}
		return cascade.Wrapf(kind, err, "%s (and the refusal could not be audited)", refusal.Error())
	}
	return refusal
}

// refuseUnconditional refuses an action the policy denies whatever is
// authorized: one the classifier puts on the top rung or cannot classify,
// which the hard verdict floor denies even past the deny list. An empty or
// elevation-class action is left to the ledger, which refuses it with its
// own code.
func (a *policyAuthorizer) refuseUnconditional(ctx context.Context, action string) error {
	if action == "" || rpc.IsElevated(action, nil) {
		return nil
	}
	level, err := a.classifier.Classify(ctx, action)
	if err == nil && level != policy.L4 {
		return nil
	}
	return cascade.New(cascade.KindPermissionDenied,
		"policy.authorize: "+sameTurnUnconditional+": the policy refuses this action unconditionally, "+
			"so a same-turn authorization cannot cover it")
}

// record appends one audit row for this call. A grant's row carries its
// nonce in Explain; a refusal (nonce "") carries none. It returns the append
// error so the grant path can withdraw and the refusal path can report it.
func (a *policyAuthorizer) record(
	ctx context.Context, subject policy.Subject, raw json.RawMessage, outcome, nonce string,
) error {
	verdict := "deny"
	if outcome == sameTurnGranted {
		verdict = "allow"
	}
	var explain json.RawMessage
	if nonce != "" {
		body, err := json.Marshal(struct {
			Nonce string `json:"nonce"`
		}{nonce})
		if err != nil {
			return err
		}
		explain = body
	}
	_, err := a.audit.Append(ctx, audit.Event{
		Kind: audit.KindPolicyDecide, Actor: string(subject.Kind) + ":" + subject.ID,
		Action: policyAuthorizeMethod, ParamsHash: audit.HashParams(raw), Verdict: verdict, Outcome: outcome,
		Explain: explain,
	})
	return err
}

// refusalCode is the stable code of a ledger refusal.
func refusalCode(err error) string {
	var ce *policy.ClassifyError
	if errors.As(err, &ce) && ce.Code != "" {
		return ce.Code
	}
	return sameTurnRefused
}

// decodeAuthorizeParams decodes raw into params, refusing unknown fields and trailing
// data so a request cannot carry anything the handler does not read.
func decodeAuthorizeParams(raw json.RawMessage, params *policyAuthorizeParams) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(params); err != nil {
		return cascade.New(cascade.KindInvalidInput, `params must be exactly {"action": "<action text>"}`)
	}
	// Anything after the object, even a stray '}' or ']' that dec.More
	// would not report, is refused: only the end of input may follow.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return cascade.New(cascade.KindInvalidInput, `params must be exactly {"action": "<action text>"}`)
	}
	return nil
}
