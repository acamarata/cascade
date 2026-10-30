//go:build !windows

package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/principal"
)

// plainMemoryLeg has no kind-narrowed search.
type plainMemoryLeg struct{ called bool }

func (l *plainMemoryLeg) SearchInScopeIncludingExpired(context.Context, string, string, int) ([]memory.IndexedRecord, error) {
	l.called = true
	return []memory.IndexedRecord{{ID: "project/x", Kind: memory.KindProject, ScopeRef: memory.DefaultScopeRef}}, nil
}

// TestOwnerOnlyMemoryLeg_KindSearchRefusals: the kind-narrowed search
// refuses a principal before any read, and refuses an owner when the inner
// leg cannot narrow by kind, never falling back to the unnarrowed search.
func TestOwnerOnlyMemoryLeg_KindSearchRefusals(t *testing.T) {
	inner := &plainMemoryLeg{}
	leg := ownerOnlyMemoryLeg{inner: inner}
	kinds := []memory.MemoryKind{memory.KindUser}
	p := principal.Principal{ID: "fake-session-principal", Kind: principal.KindSession, SessionID: "fake-session"}
	pctx := principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	if rows, err := leg.SearchInScopeKindsIncludingExpired(pctx, "q", memory.DefaultScopeRef, kinds, 5); err != errNotInPrincipalScope || rows != nil {
		t.Fatalf("principal: rows %v err %v, want errNotInPrincipalScope", rows, err)
	}
	octx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	rows, err := leg.SearchInScopeKindsIncludingExpired(octx, "q", memory.DefaultScopeRef, kinds, 5)
	if err == nil || rows != nil || inner.called {
		t.Fatalf("owner over a plain leg: rows %v err %v called %v, want a refusal and no read", rows, err, inner.called)
	}
}
