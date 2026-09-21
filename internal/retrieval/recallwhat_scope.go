// Purpose: the E/S-08.T4 session-scope seam recall.what's composition
// root resolves the caller's scope through, and the one narrowing string
// every domain leg is checked against. Split out of recallwhat.go purely
// for the 300-line cap; same concern (the fan-out), not a different one.
//
// Inputs: the caller's raw session signals (scope.ResolveInput: cwd,
// user, machine, branch, task, session, explicit_overrides) and an
// injected ScopeResolver.
//
// Outputs: a scope reference string derived from the resolved
// scope.SessionScope, or a taxonomy error.
//
// Constraints: the resolver is the ONLY authority on what a session's
// scope actually is (R-16.3's deny-by-default graph traversal). A
// caller-supplied `scope` field on the wire is never trusted on its own —
// it is compared against the resolved value and refused on disagreement,
// never silently honoured (D5.1, adversarial-review fix item 1).
//
// SPORT: internal.retrieval.RecallWhatService/CHANGED (P1-E22-W5-S47-T1
// rework).

package retrieval

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ScopeResolver resolves the caller's SessionScope from its raw session
// signals. It is exactly scope.ResolveSessionScope's own shape, declared
// here as a one-method interface so this package depends on the question
// rather than on scope.GraphStore/GitRootFunc construction details — the
// composition root wires the real scope.ResolveDeps behind it (D1: the
// same resolver context.scope.show already uses, per rpc.go's
// ContextScopeShow), and a test injects a fake that returns a canned
// SessionScope with no database at all.
type ScopeResolver interface {
	Resolve(ctx context.Context, in scope.ResolveInput) (scope.SessionScope, error)
}

// scopeRefFor derives the single scope reference every domain leg is
// narrowed to from a resolved SessionScope.
//
// ScopeKindGeneral (no repository bound to the resolved git root, R-16.3's
// documented restricted-but-successful result) has no Project to narrow
// to; it resolves to the empty scope reference, which every leg's own
// narrowing treats as "matches nothing" rather than "matches everything" —
// an unresolved session is not a reason to widen what it can read.
func scopeRefFor(s scope.SessionScope) string {
	if s.Kind == scope.ScopeKindGeneral {
		return ""
	}
	return s.Project
}

// resolveRequestScope resolves req's raw session signals and checks any
// caller-asserted Scope against the result, refusing a mismatch with
// KindInvalidInput rather than silently honouring the caller's claim
// (D5.1). An empty caller-asserted Scope is not a mismatch: it is the
// common case of a caller that never guessed and is trusting resolution
// entirely.
func resolveRequestScope(ctx context.Context, resolver ScopeResolver, req RecallWhatRequest) (string, error) {
	if resolver == nil {
		return "", cascade.New(cascade.KindUnavailable, "recall.what: no scope resolver is configured")
	}
	resolved, err := resolver.Resolve(ctx, req.ResolveIn)
	if err != nil {
		return "", err
	}
	ref := scopeRefFor(resolved)
	if req.Scope != "" && req.Scope != ref {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"recall.what: the requested scope %q does not match the resolved session scope %q", req.Scope, ref)
	}
	return ref, nil
}
