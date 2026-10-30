//go:build !windows

// Purpose: the general-session memory search on recall.what's owner-only
// memory leg, split from daemon_unix_recall_what.go for the 300-line cap.
//
// Inputs: the caller on ctx, a query, the store scope and the kind
// allowlist chosen by internal/retrieval.
//
// Outputs: the inner projection's kind-narrowed rows, or a refusal.
//
// Constraints: a principal is refused before any read, exactly as on the
// leg's other method. An inner leg that cannot narrow by kind refuses
// rather than widening to every kind.

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SearchInScopeKindsIncludingExpired implements
// retrieval.RecallWhatGeneralMemoryLeg.
func (l ownerOnlyMemoryLeg) SearchInScopeKindsIncludingExpired(
	ctx context.Context, query, scopeRef string, kinds []memory.MemoryKind, limit int,
) ([]memory.IndexedRecord, error) {
	if _, isPrincipal := principalCaller(ctx); isPrincipal {
		return nil, errNotInPrincipalScope
	}
	kl, ok := l.inner.(retrieval.RecallWhatGeneralMemoryLeg)
	if !ok {
		return nil, cascade.New(cascade.KindUnavailable, "recall.what: the memory leg cannot narrow by kind")
	}
	return kl.SearchInScopeKindsIncludingExpired(ctx, query, scopeRef, kinds, limit)
}
