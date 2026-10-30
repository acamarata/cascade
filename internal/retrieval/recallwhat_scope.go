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
	"github.com/acamarata/cascade/internal/memory"
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
// to; it resolves to the empty scope reference, the general bucket. The
// conversation leg narrows to "" by equality, never as "any scope": a
// general session reads only threads whose creating call resolved to no
// project, and a thread with no project row at all matches no narrowed
// query, general or not. The files and memory legs narrow to
// storeScopeRefFor instead. An unresolved session is not a reason to
// widen what it can read.
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
	ref, _, err := resolveRequestScopes(ctx, resolver, req)
	return ref, err
}

// resolveRequestScopes is resolveRequestScope plus the files and memory
// legs' scope (storeScopeFor), both taken from this one resolution. The
// conversation leg re-resolves on its own; a disagreement there refuses
// that leg, never widens it.
func resolveRequestScopes(ctx context.Context, resolver ScopeResolver, req RecallWhatRequest) (string, storeScope, error) {
	if resolver == nil {
		return "", storeScope{}, cascade.New(cascade.KindUnavailable, "recall.what: no scope resolver is configured")
	}
	resolved, err := resolver.Resolve(ctx, req.ResolveIn)
	if err != nil {
		return "", storeScope{}, err
	}
	ref := scopeRefFor(resolved)
	if req.Scope != "" && req.Scope != ref {
		return "", storeScope{}, cascade.Newf(cascade.KindInvalidInput,
			"recall.what: the requested scope %q does not match the resolved session scope %q", req.Scope, ref)
	}
	return ref, storeScopeFor(resolved), nil
}

// storeScope is what the files and memory legs narrow to: one store
// reference, and whether the session is general.
type storeScope struct {
	ref     string
	general bool
}

// generalMemoryKinds is the closed allowlist of memory kinds a general
// session may read (R-16.3: user tiers, NO project state): facts about the
// person and their stated preferences. KindProject is project state.
// KindReference is excluded too: looked-up material carries no tier of its
// own and is often a project's. A new kind is excluded until listed here.
// A function, not a package variable, so no caller can widen it.
func generalMemoryKinds() []memory.MemoryKind {
	return []memory.MemoryKind{memory.KindUser, memory.KindFeedback}
}

// storeScopeFor derives the files and memory legs' scope. Neither store
// can hold an empty reference (corpus.ScopeRef.Valid, memory types.go).
// Owner content written with no per-project resolver, the configured
// retrieval sources and `cascade memory remember`, is filed under the
// owner bucket memory.DefaultScopeRef from any session, so that bucket
// pools project material. A general session reads that bucket, narrowed
// to non-project content: the files leg admits only personal and global
// privacy (corpus.Query.GeneralSession) and the memory leg only
// generalMemoryKinds. The mapping keys on Kind, never on an empty
// Project: any other kind with no Project keeps "", which the files leg
// refuses and the memory leg matches nothing with (fail closed).
func storeScopeFor(s scope.SessionScope) storeScope {
	if s.Kind == scope.ScopeKindGeneral {
		return storeScope{ref: memory.DefaultScopeRef, general: true}
	}
	return storeScope{ref: s.Project}
}
