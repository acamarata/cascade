//go:build !windows

// Purpose: the cwd resolver the chat.* namespace records a new owner thread's
// project through (P1-CPA-06, contract:thread-scope).
// Inputs: the daemon's path provider.
// Outputs: a func mapping a cwd to the same project ref recall.what narrows
// by (retrieval.scopeRefFor: ScopeKindGeneral is "", otherwise the resolved
// Project), through the very resolver recall.what binds, so one cwd names one
// project on both surfaces.
// Constraints: built lazily, on the first owner thread that carries a cwd,
// so wiring chat opens no extra connection and cannot fail the daemon start;
// a construction or resolution failure is returned to the caller, which
// leaves the thread unscoped (no row), never widened. A resolution that is
// neither general nor names a project is an error too: "" must only ever mean
// the general bucket. Windows has no resolver (the twin file returns nil).
// SPORT: cmd/cascade chat scope resolver (ADD) -- P1-CPA-06.
package main

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// chatScopeResolver returns the chat adapter's project resolver.
func chatScopeResolver(paths runtime.PathProvider) func(context.Context, scope.ResolveInput) (string, error) {
	var (
		once     sync.Once
		resolver retrieval.ScopeResolver
		buildErr error
	)
	return func(ctx context.Context, in scope.ResolveInput) (string, error) {
		once.Do(func() { resolver, buildErr = recallWhatScopeResolver(paths) })
		if buildErr != nil {
			return "", buildErr
		}
		resolved, err := resolver.Resolve(ctx, in)
		if err != nil {
			return "", err
		}
		return chatProjectRef(resolved)
	}
}

// chatProjectRef is retrieval.scopeRefFor, with the one difference that a
// resolved scope naming no project is refused instead of read as general.
func chatProjectRef(s scope.SessionScope) (string, error) {
	if s.Kind == scope.ScopeKindGeneral {
		return "", nil
	}
	if s.Project == "" {
		return "", cascade.New(cascade.KindInvalidInput, "chat: the resolved session scope names no project")
	}
	return s.Project, nil
}
