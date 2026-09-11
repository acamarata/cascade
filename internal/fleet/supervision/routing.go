package supervision

// Purpose (this file): the R-21.157 closes this ticket's task 8 left
// open: (a) VISIBILITY for list/get, resolved through the E/S-08.T4
// closed traversal table per hop, never an ad-hoc chain comparison and
// never post-query filtering; (b) ADDRESSING IS ROUTING, not
// authorization — an addressed push (target scope != origin scope)
// requires inbox.send on the origin plus inbox.cross_scope_send on the
// target (or a declassification token), the payload data-class check,
// and — for the global scope — the closed kind set with the minimal
// schema.
//
// Inputs: a *scope.GraphStore (visibility), a GrantChecker duck-typed
// against the I/S-17.T2 evaluator (internal/policy.StoreGrants.Check —
// already an established W2 surface; no new dependency edge, per the
// ticket's own task 8 note), and a DataClassChecker for the payload
// check.
// Outputs: the resolved []ScopeRef candidate set, or a typed
// cascade.KindPermissionDenied refusal from RoutePush.
// Constraints: this file defines NO new capability name — "inbox.send"
// and "inbox.cross_scope_send" are looked up by name only; if the
// registry does not hold them (I/S-17.T1 has not seeded them as of this
// ticket — grep across the tree found zero occurrences of either string
// outside this file and its test, recorded in the journal), Check
// answers capability-not-found, which this file already maps to the
// SAME fail-closed KindPermissionDenied refusal a real deny would
// produce. Nothing here papers over that gap or invents a permissive
// fallback for it.
//
// SPORT: fleet.supervision.RoutePush/ADDED,
//        fleet.supervision.ResolveVisibleScopes/ADDED (P1-E18-W4-S39-T1).

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The two capability names R-21.157(b) names for an addressed push. Their
// registration is I/S-17.T1's concern; this file only looks them up.
const (
	capabilityInboxSend           = "inbox.send"
	capabilityInboxCrossScopeSend = "inbox.cross_scope_send"
)

// GrantCheckRequest mirrors the shape GrantChecker.Check needs, declared
// locally so this package does not import internal/policy's Subject/
// CheckRequest types directly (the seam stays duck-typed, matching
// internal/fleet/journal's RPCCaller precedent for the identical
// reason — this package's tests inject a fake without constructing a
// real policy.Engine).
type GrantCheckRequest struct {
	SubjectKind string
	SubjectID   string
	Capability  string
}

// GrantChecker is the minimal seam RoutePush authorizes an addressed push
// through — internal/policy.StoreGrants already satisfies this shape via
// a thin adapter the composition root supplies (out of this ticket's
// files_scope, exactly as internal/policy's own consumers wire it
// elsewhere).
type GrantChecker interface {
	// Check reports whether req's subject holds req.Capability. A
	// capability the registry does not hold, or a subject with no grant,
	// returns ok=false with no error — RoutePush's caller cannot
	// distinguish "denied" from "unknown" and must not try to (fail
	// closed either way). A genuine transport/storage error returns a
	// non-nil error.
	Check(ctx context.Context, req GrantCheckRequest) (ok bool, err error)
}

// DataClassChecker validates a push's payload against the data-class
// policy (R-21.157(b)'s "the data-class check applies to the payload
// before delivery"). Declared as a seam since the data-class taxonomy
// belongs to another ticket; a nil DataClassChecker passed to RoutePush
// is treated as "no data-class policy configured" and always passes —
// documented explicitly so a caller cannot mistake nil for "denies
// everything."
type DataClassChecker interface {
	Allow(ctx context.Context, item AttentionItem) (ok bool, err error)
}

// PushRequest is an addressed push: item plus the origin scope the push
// originates from. item.ScopeRef is the ADDRESSED (target) scope — the
// delivery destination — which may differ from origin.
type PushRequest struct {
	Item   AttentionItem
	Origin ScopeRef
	// Subject is who is performing the push (the capability check's
	// subject).
	Subject GrantCheckRequest
}

// errRouteDenied is A-T7's typed permission refusal for every RoutePush
// failure leg (missing capability, failing data-class check, or an
// out-of-set kind on a global-scope item): KindPermissionDenied, per the
// ticket's own instruction ("refuses the push with an A-T7 typed
// permission error").
func errRouteDenied(reason string) error {
	return cascade.New(cascade.KindPermissionDenied, "supervision: addressed push refused: "+reason)
}

// RoutePush authorizes and then performs an addressed push through
// store. Every failure leg below QUEUES NOTHING (fail-closed): the
// capability checks and the data-class check all run BEFORE store.Push
// is ever called.
func RoutePush(ctx context.Context, store *Store, checker GrantChecker, dataClass DataClassChecker, req PushRequest) (AttentionItem, error) {
	if err := authorizeAddressedPush(ctx, checker, req); err != nil {
		return AttentionItem{}, err
	}
	if err := checkDataClass(ctx, dataClass, req.Item); err != nil {
		return AttentionItem{}, err
	}
	if req.Item.ScopeRef.Kind == scope.ScopeKindGlobal {
		if err := checkGlobalShape(req.Item); err != nil {
			return AttentionItem{}, err
		}
	}
	return store.Push(ctx, req.Item)
}

// authorizeAddressedPush applies R-21.157(b)'s capability rule: a push
// whose target (req.Item.ScopeRef) differs from its Origin needs
// inbox.send on Origin plus inbox.cross_scope_send on the target.
// Same-scope pushes (target == origin) are routing-neutral and need
// neither capability — R-21.157(b) governs ADDRESSED delivery only.
func authorizeAddressedPush(ctx context.Context, checker GrantChecker, req PushRequest) error {
	if req.Item.ScopeRef == req.Origin {
		return nil
	}
	if checker == nil {
		return errRouteDenied("no capability evaluator configured for a cross-scope push")
	}
	sendReq := GrantCheckRequest{SubjectKind: req.Subject.SubjectKind, SubjectID: req.Subject.SubjectID, Capability: capabilityInboxSend}
	ok, err := checker.Check(ctx, sendReq)
	if err != nil {
		return err
	}
	if !ok {
		return errRouteDenied("subject lacks inbox.send on the origin scope")
	}
	crossReq := GrantCheckRequest{SubjectKind: req.Subject.SubjectKind, SubjectID: req.Subject.SubjectID, Capability: capabilityInboxCrossScopeSend}
	ok, err = checker.Check(ctx, crossReq)
	if err != nil {
		return err
	}
	if !ok {
		return errRouteDenied("subject lacks inbox.cross_scope_send on the target scope")
	}
	return nil
}

// checkDataClass runs dataClass.Allow on item, if configured. A nil
// dataClass always passes — see DataClassChecker's doc comment.
func checkDataClass(ctx context.Context, dataClass DataClassChecker, item AttentionItem) error {
	if dataClass == nil {
		return nil
	}
	ok, err := dataClass.Allow(ctx, item)
	if err != nil {
		return err
	}
	if !ok {
		return errRouteDenied("payload failed the data-class check")
	}
	return nil
}

// checkGlobalShape enforces R-21.157(b)'s global-scope restriction: the
// item's Kind must be in the closed set (currently identical to Kind's
// own four members, declared separately in attention.go's globalKindSet
// so a future Kind widening does not silently widen this too) and it
// must carry no free-text body. AttentionItem has no free-text body
// field at all (its fields are exactly {id, kind, source_ref, scope_ref,
// priority, created_at, acked_at} — see attention.go), so the schema
// half of this rule is satisfied by construction; this function checks
// the kind-set half, which is not automatic.
func checkGlobalShape(item AttentionItem) error {
	if !globalKindSet[item.Kind] {
		return errRouteDenied("global-scope item kind is outside the closed set")
	}
	return nil
}

// ResolveVisibleScopes implements R-21.157(a): it resolves the candidate
// scope set chain may see by reading the E/S-08.T4 traversal table
// (scope.CandidateScopeRefs) — this function is a thin, undecorated call
// into that table, never a re-derivation of direction/transitivity. An
// unresolvable chain (nil/empty) collapses to the caller's own session
// scope only, per the ticket's own fallback rule ("a caller whose scope
// cannot be resolved sees only its own session scope").
func ResolveVisibleScopes(ctx context.Context, store *scope.GraphStore, chain []ScopeRef, ownScope ScopeRef) ([]ScopeRef, error) {
	if store == nil || len(chain) == 0 {
		return []ScopeRef{ownScope}, nil
	}
	refs, err := scope.CandidateScopeRefs(ctx, store, chain)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []ScopeRef{ownScope}, nil
	}
	return refs, nil
}
