package nodes

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the controller-side entry point for a remote
//
//	dispatch — "node.dispatch" on the daemon's RPC router, over the real
//	ship leg.
//
// Inputs: a DispatchRequest over RPC, and a resolver the composition root
//
//	supplies for the ship dependencies (git, the node caller, the fencing
//	register, the sequence store).
//
// Outputs: the commit the node's results landed at, or a typed refusal.
// Constraints: this is what makes the dispatch engine reachable from a
//
//	running program rather than only from its own tests (Art.10.5). The
//	DEPS are resolved per call rather than captured at registration, for
//	the reason node_upgrade_rpc.go resolves its own: a dispatch needs a
//	live git remote and a reachable node, and binding those at daemon
//	start would register a handler that fails for the rest of the process's
//	life if either moved.
//
//	The seam is an RPC verb, NOT a new CLI verb: 07 §node defines none, and
//	the requirement surface stays `cascade run --require node.<cap>=true`
//	(07 §run). The router selects, S-37.T1's placement filters, and this
//	ships what survives both.
//
// SPORT: internal/nodes:dispatch-handler (ADD) — P1-E17-W4-S37-T2.

// DispatchMethod is the RPC verb this handler mounts.
const DispatchMethod = "node.dispatch"

// DispatchRequest is one remote-dispatch call.
type DispatchRequest struct {
	// DispatchID identifies the work across attempts.
	DispatchID string `json:"dispatch_id"`
	// NodeID names the node placement selected.
	NodeID string `json:"node_id"`
	// Head is the commit the work branch is cut from.
	Head string `json:"head"`
	// ActionID is the dispatched action's stable identity, which the node
	// records durably before executing so a redelivery is refused.
	ActionID string `json:"action_id"`
	// Idempotent declares whether the action may be re-queued
	// automatically after an ambiguous outcome.
	Idempotent bool `json:"idempotent"`
	// Sensitivity is the work's resolved class.
	Sensitivity string `json:"sensitivity"`
	// Credential names how this lane is authorized: "none", "static" or
	// "scoped" (§D-11). An unrecognized value relays through the
	// controller rather than being guessed at.
	Credential string `json:"credential,omitempty"`
	// Audience, VaultKey and Verbs bind a scoped per-dispatch token
	// (R-21.222). They are ignored for the other kinds.
	Audience string   `json:"audience,omitempty"`
	VaultKey string   `json:"vault_key,omitempty"`
	Verbs    []string `json:"verbs,omitempty"`
	// Payload is the work description the node receives.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// DispatchResult is what the controller reports back.
type DispatchResult struct {
	// Commit is where the node's results landed.
	Commit string `json:"commit"`
	// Attempt is the fencing number the work shipped under.
	Attempt uint64 `json:"attempt"`
	// Relayed reports that the lane's calls are made by the controller on
	// the node's behalf rather than by the node itself, which is every
	// static-key lane (§D-11). A caller that does not see this cannot
	// tell a node that was trusted with a token from one that was not.
	Relayed bool `json:"relayed"`
}

// dispatchHandlerDeps resolves the ship dependencies and the device record
// for the selected node.
type dispatchHandlerDeps func(ctx context.Context, nodeID string) (ShipDeps, DeviceRecord, error)

// RegisterDispatchHandler mounts DispatchMethod on registry.
func RegisterDispatchHandler(registry *rpc.Registry, resolve dispatchHandlerDeps) {
	registry.Register(DispatchMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		req, err := decodeDispatchRequest(params)
		if err != nil {
			return nil, err
		}
		deps, record, err := resolve(ctx, req.NodeID)
		if err != nil {
			return nil, err
		}
		// §D-11 runs BEFORE the ship leg: the plan is what decides
		// whether anything may be carried at all, and a lane that must
		// relay must never have had a token minted for it.
		plan, err := PlanCredentials(CredentialKind(req.Credential), req.DispatchID, req.NodeID,
			req.Audience, req.VaultKey, req.Verbs, deps.now(), MaxTokenLifetime)
		if err != nil {
			return nil, err
		}
		outcome, err := Ship(ctx, deps, record, ShipRequest{
			DispatchID:  req.DispatchID,
			Head:        req.Head,
			Work:        Action{ID: req.ActionID, Idempotent: req.Idempotent},
			Sensitivity: Sensitivity(req.Sensitivity),
			Payload:     req.Payload,
		})
		if err != nil {
			return nil, err
		}
		return DispatchResult{
			Commit:  outcome.Commit,
			Attempt: outcome.Attempt,
			Relayed: plan.RelayThroughController,
		}, nil
	})
}

// decodeDispatchRequest decodes and validates one call's parameters.
//
// The required fields are checked HERE rather than being left to Ship,
// because a missing node id or dispatch id is a caller mistake and naming
// it is more useful than the downstream failure it would otherwise become.
func decodeDispatchRequest(params json.RawMessage) (DispatchRequest, error) {
	var req DispatchRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return req, cascade.Wrap(cascade.KindInvalidInput, err,
				"nodes: "+DispatchMethod+": decode request")
		}
	}
	for field, value := range map[string]string{
		"dispatch_id": req.DispatchID,
		"node_id":     req.NodeID,
		"action_id":   req.ActionID,
	} {
		if value == "" {
			return req, cascade.Newf(cascade.KindInvalidInput,
				"nodes: %s requires %s", DispatchMethod, field)
		}
	}
	return req, nil
}

// Dispatcher holds the controller's per-PROCESS dispatch state.
//
// The fencing register and the sequence store must be shared across every
// dispatch this controller makes, not rebuilt per call. A per-call register
// would hand out attempt 1 every time, so no attempt would ever supersede
// another and ErrStaleAttempt could never fire — fencing would still be
// there in the code and absent in fact. This type exists so that sharing is
// something a caller does by construction rather than by remembering to.
type Dispatcher struct {
	attempts  *AttemptRegister
	sequences *SequenceStore
}

// NewDispatcher builds a controller's dispatch state.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{attempts: NewAttemptRegister(), sequences: NewSequenceStore()}
}

// ShipDepsFor builds the ship dependencies for one call over this
// controller's shared fencing state.
func (d *Dispatcher) ShipDepsFor(repoRoot, remote string, git GitRunner, caller NodeCaller, clock Clock) ShipDeps {
	return ShipDeps{
		Git:       newDispatchGit(repoRoot, git),
		Caller:    caller,
		Attempts:  d.attempts,
		Sequences: d.sequences,
		Remote:    remote,
		Clock:     clock,
	}
}

// JournalDepsFor builds the journal-stream dependencies over the same
// fencing register, so a streamed record is fenced against the very attempt
// the ship leg minted.
func (d *Dispatcher) JournalDepsFor(sink NodeStreamAppender) JournalStreamDeps {
	return JournalStreamDeps{Attempts: d.attempts, Sink: sink}
}

// The two verbs the NODE calls over its own tunnel. They live on the
// controller's registry because the D-24 tunnel is a reverse forward: the
// node connects out and its calls arrive here (dispatch_rendezvous.go).
const (
	// DispatchClaimMethod is how a node asks for the work placed on it.
	DispatchClaimMethod = "node.dispatch.claim"
	// DispatchReportMethod is how a node returns its signed result frame.
	DispatchReportMethod = "node.dispatch.report"
)

// ClaimRequest identifies the node asking for work.
type ClaimRequest struct {
	NodeID string `json:"node_id"`
}

// ClaimResponse is the attempt a node was handed.
type ClaimResponse struct {
	DispatchID string `json:"dispatch_id"`
	Attempt    uint64 `json:"attempt"`
	Branch     string `json:"branch"`
}

// RegisterDispatchNodeHandlers mounts the node-facing claim and report
// verbs against rv.
//
// Neither verb trusts its caller's word about which dispatch it is
// answering: Claim hands out only the attempt placed on THAT node, and
// Report is matched on {dispatch, attempt, node} before the frame is
// admitted — the signature check itself runs later, on the ship leg, where
// the device record and sequence store live.
func RegisterDispatchNodeHandlers(registry *rpc.Registry, rv *Rendezvous) {
	registry.Register(DispatchClaimMethod, func(_ context.Context, params json.RawMessage) (any, error) {
		var req ClaimRequest
		if err := decodeParams(params, &req, DispatchClaimMethod); err != nil {
			return nil, err
		}
		attempt, err := rv.Claim(req.NodeID)
		if err != nil {
			return nil, err
		}
		return ClaimResponse{
			DispatchID: attempt.DispatchID,
			Attempt:    attempt.Attempt,
			Branch:     attempt.Branch,
		}, nil
	})

	registry.Register(DispatchReportMethod, func(_ context.Context, params json.RawMessage) (any, error) {
		var frame DispatchFrame
		if err := decodeParams(params, &frame, DispatchReportMethod); err != nil {
			return nil, err
		}
		if err := rv.Report(frame); err != nil {
			return nil, err
		}
		return map[string]bool{"accepted": true}, nil
	})
}

// DispatchJournalMethod is how a node streams journal records back.
const DispatchJournalMethod = "node.dispatch.journal"

// RegisterDispatchJournalHandler mounts the journal-stream verb over deps.
//
// It is separate from RegisterDispatchNodeHandlers because its dependency
// is different in kind: claim and report need only the rendezvous, while
// this needs the controller's journal store, which the composition root
// opens. Mounting it separately means a daemon without a journal store
// still serves the other two rather than failing to register any of them.
func RegisterDispatchJournalHandler(registry *rpc.Registry, deps JournalStreamDeps) {
	registry.Register(DispatchJournalMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		var rec JournalRecord
		if err := decodeParams(params, &rec, DispatchJournalMethod); err != nil {
			return nil, err
		}
		if err := StreamJournalRecord(ctx, deps, rec); err != nil {
			return nil, err
		}
		return map[string]bool{"appended": true}, nil
	})
}

// decodeParams decodes one call's params, naming the verb in any failure.
func decodeParams(params json.RawMessage, into any, method string) error {
	if len(params) == 0 {
		return cascade.Newf(cascade.KindInvalidInput, "nodes: %s requires params", method)
	}
	if err := json.Unmarshal(params, into); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "nodes: %s: decode request", method)
	}
	return nil
}
