// Package policy (rpc.go): Purpose: the approval.* / standing_grant.* /
//
//	policy.* JSON-RPC handler set — the outside edge of Epic I. Every
//	handler here decodes its params, passes through the ONE Authorize
//	middleware (R-21.207) and then calls the already-built queue, engine,
//	grant store and audit log. It adds no second guard of its own.
//
// Inputs: RPCDeps, the collaborators a composition root has already built,
//
//	and raw JSON params from the socket.
//
// Outputs: MethodHandlers, the method-name to handler map a registration
//
//	site binds into the daemon's method registry; MethodFunc; RPCDeps.
//
// Constraints: FAIL CLOSED everywhere. An unregistered verb refuses before
//
//	any collaborator is touched; a collaborator this process never wired
//	refuses with KindUnavailable rather than being replaced by a
//	permissive default; unparseable params refuse. No bare time.Now: the
//	only clock is the injected one the collaborators already hold. Errors
//	come from pkg/cascade's frozen taxonomy only.
//
// SPORT: internal/policy rpc-handlers/ADDED (P1-E09-W2-S18-T6).
package policy

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodFunc is one dispatched method call: raw params in, a result value
// out. It is structurally identical to the daemon registry's handler type
// and is declared here so this package never imports the RPC server.
type MethodFunc func(ctx context.Context, params json.RawMessage) (any, error)

// AuditQuerier is the read half of the append-only audit log. *audit.Log
// satisfies it; the dependency direction is policy to audit, never back.
type AuditQuerier interface {
	// Query returns the records matching f, oldest first.
	Query(ctx context.Context, f audit.Filter) (audit.Page, error)
}

// RPCDeps are the already-built collaborators the handler set closes over.
// A nil member is not a default: the verbs that need it refuse.
type RPCDeps struct {
	// Queue is the approval queue the ask verdicts of this process land in.
	Queue ApprovalQueue
	// Engine is the one production policy engine.
	Engine *Engine
	// Registry is the boot-loaded capability registry.
	Registry CapabilityRegistry
	// Grants is the B-layer grant store standing grants are written to.
	Grants GrantStore
	// DenyList is the B-layer deny-list engine CreateStandingGrant's
	// first guard consults.
	DenyList DenyListEngine
	// Audit is the append-only audit log's read half.
	Audit AuditQuerier
	// Verifier verifies the signed approval token approval.grant carries.
	// Absent, approval.grant refuses: approval authority is never
	// reducible to knowing a request id (R-21.230).
	Verifier *ApprovalVerifier
	// Clock is the injected time source.
	Clock Clock
	// Attestor vouches for a fresh local attestation on elevated verbs.
	Attestor Attestor
}

// MethodHandlers returns the handler for every verb in the registry,
// keyed by method name. The map's key set is asserted against
// RegisteredVerbs by this package's own tests, so a verb added to one and
// not the other is a build-time-visible defect rather than a method that
// dispatches nowhere.
func MethodHandlers(deps RPCDeps) map[string]MethodFunc {
	handlers := map[string]MethodFunc{
		"approval.list":         deps.guard("approval.list", deps.approvalList),
		"approval.show":         deps.guard("approval.show", deps.approvalShow),
		"approval.grant":        deps.guard("approval.grant", deps.approvalGrant),
		"approval.deny":         deps.guard("approval.deny", deps.approvalDeny),
		"approval.expire":       deps.guard("approval.expire", deps.approvalExpire),
		"standing_grant.list":   deps.guard("standing_grant.list", deps.standingList),
		"standing_grant.create": deps.guard("standing_grant.create", deps.standingCreate),
		"standing_grant.change": deps.guard("standing_grant.change", deps.standingChange),
		"standing_grant.revoke": deps.guard("standing_grant.revoke", deps.standingRevoke),
		"policy.explain":        deps.guard("policy.explain", deps.policyExplain),
		"policy.check":          deps.guard("policy.check", deps.policyCheck),
		"policy.list":           deps.guard("policy.list", deps.policyList),
		"policy.audit_query":    deps.guard("policy.audit_query", deps.policyAuditQuery),
	}
	return handlers
}

// guard wraps next with the one authorization middleware. Every handler in
// the set above is wrapped: there is no path into this package's RPC
// surface that skips Authorize.
func (d RPCDeps) guard(method string, next MethodFunc) MethodFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if _, err := Authorize(ctx, method, d.Attestor); err != nil {
			return nil, err
		}
		return next(ctx, params)
	}
}

// decodeParams unmarshals params into dst. Absent params decode as the
// zero value, which each handler then validates for itself; params that
// are present and malformed REFUSE rather than falling back to the zero
// value, since a caller that sent something meant something.
func decodeParams(params json.RawMessage, dst any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "policy: params are not readable")
	}
	return nil
}

// requireQueue returns the queue or a refusal naming what is missing.
func (d RPCDeps) requireQueue() (ApprovalQueue, error) {
	if d.Queue == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"policy: no approval queue is wired in this process")
	}
	return d.Queue, nil
}

// requireEngine returns the engine or a refusal naming what is missing.
func (d RPCDeps) requireEngine() (*Engine, error) {
	if d.Engine == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"policy: no policy engine is wired in this process")
	}
	return d.Engine, nil
}

// ApprovalListResult is the approval.list response. It carries
// PendingEntry values and nothing else, so no token, nonce or action hash
// can reach a caller through this surface (§5.24).
type ApprovalListResult struct {
	// Pending are the entries awaiting a decision, oldest first.
	Pending []PendingEntry `json:"pending"`
}

// approvalList returns the pending queue.
func (d RPCDeps) approvalList(ctx context.Context, _ json.RawMessage) (any, error) {
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	pending, err := queue.GetPending(ctx)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		pending = []PendingEntry{}
	}
	return ApprovalListResult{Pending: pending}, nil
}

// ApprovalShowParams names one queued entry.
type ApprovalShowParams struct {
	// RequestID is the entry's identifier.
	RequestID string `json:"request_id"`
}

// approvalShow returns one pending entry. An id that names nothing is
// KindNotFound; an empty id is KindInvalidInput, because an absent id is a
// malformed request rather than a miss.
func (d RPCDeps) approvalShow(ctx context.Context, params json.RawMessage) (any, error) {
	var p ApprovalShowParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval.show requires a request_id")
	}
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	pending, err := queue.GetPending(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range pending {
		if entry.RequestID == p.RequestID {
			return entry, nil
		}
	}
	return nil, cascade.Newf(cascade.KindNotFound,
		"policy: no pending approval has request id %q", sanitize(p.RequestID))
}

// ApprovalDecisionParams is the deny verb's params. The presented summary
// and rung are required by the queue itself: a decision is bound to what
// the surface actually displayed.
type ApprovalDecisionParams struct {
	// RequestID names the entry being decided.
	RequestID string `json:"request_id"`
	// PresentedSummary is the exact string the surface displayed.
	PresentedSummary string `json:"presented_summary"`
	// PresentedLevel is the rung the surface displayed.
	PresentedLevel RiskLevel `json:"presented_level"`
}

// approvalDeny records one refusal and reports the state it produced.
func (d RPCDeps) approvalDeny(ctx context.Context, params json.RawMessage) (any, error) {
	var p ApprovalDecisionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval.deny requires a request_id")
	}
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	outcomes, err := queue.Decide(ctx, []DecisionRequest{{
		RequestID:        p.RequestID,
		Approved:         false,
		PresentedSummary: p.PresentedSummary,
		PresentedLevel:   p.PresentedLevel,
	}})
	if err != nil {
		return nil, err
	}
	if len(outcomes) != 1 {
		return nil, cascade.New(cascade.KindInternal,
			"policy: the approval queue answered a one-element decision with a different number of outcomes")
	}
	if outcomes[0].Err != nil {
		return nil, outcomes[0].Err
	}
	return DecisionResult{RequestID: outcomes[0].RequestID, State: outcomes[0].State.String()}, nil
}

// DecisionResult reports what a mutating decision verb changed.
type DecisionResult struct {
	// RequestID names the entry.
	RequestID string `json:"request_id"`
	// State is the entry's state after the call.
	State string `json:"state"`
}

// ApprovalExpireResult reports how many entries the sweep retired.
type ApprovalExpireResult struct {
	// Expired is the number of entries retired by this sweep.
	Expired int `json:"expired"`
}

// approvalExpire runs the rate-limited expiry sweep.
func (d RPCDeps) approvalExpire(ctx context.Context, _ json.RawMessage) (any, error) {
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	n, err := queue.Expire(ctx)
	if err != nil {
		return nil, err
	}
	return ApprovalExpireResult{Expired: n}, nil
}
