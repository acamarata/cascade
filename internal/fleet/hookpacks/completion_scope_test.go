// Purpose: exercises ResolveJobID's three outcomes directly (R-21.176):
// no id in the payload, a resolver error while scope cannot be
// determined, and a resolver-reported unknown job id.
// SPORT: fleet/hookpacks.JobResolver/ResolveJobID/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResolveJobID_NoIdentityIsUnscoped(t *testing.T) {
	resolver := fakeResolver{jobs: map[string]bool{}}
	id, scoped, err := hookpacks.ResolveJobID(context.Background(), hookpacks.CompletionHookPayload{SessionID: "s1"}, resolver)
	if scoped || id != "" || err != nil {
		t.Fatalf("ResolveJobID(no identity) = (%q, %v, %v), want (\"\", false, nil)", id, scoped, err)
	}
}

func TestResolveJobID_ResolverErrorIsUnscopedNotDenied(t *testing.T) {
	resolver := fakeResolver{err: cascade.New(cascade.KindUnavailable, "daemon unreachable")}
	id, scoped, err := hookpacks.ResolveJobID(context.Background(), hookpacks.CompletionHookPayload{JobID: "job-1"}, resolver)
	if scoped || id != "" || err == nil {
		t.Fatalf("ResolveJobID(resolver unavailable) = (%q, %v, %v), want (\"\", false, non-nil)", id, scoped, err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ResolveJobID(resolver unavailable) err = %v, want KindUnavailable preserved", err)
	}
}

func TestResolveJobID_UnknownJobIsScopedAndErrors(t *testing.T) {
	resolver := fakeResolver{jobs: map[string]bool{}}
	id, scoped, err := hookpacks.ResolveJobID(context.Background(), hookpacks.CompletionHookPayload{JobID: "ghost"}, resolver)
	if !scoped || id != "ghost" || err == nil {
		t.Fatalf("ResolveJobID(unknown job) = (%q, %v, %v), want (\"ghost\", true, non-nil)", id, scoped, err)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("ResolveJobID(unknown job) err = %v, want KindNotFound", err)
	}
}

func TestResolveJobID_KnownJobResolves(t *testing.T) {
	resolver := fakeResolver{jobs: map[string]bool{"job-1": true}}
	id, scoped, err := hookpacks.ResolveJobID(context.Background(), hookpacks.CompletionHookPayload{JobID: "job-1"}, resolver)
	if !scoped || id != "job-1" || err != nil {
		t.Fatalf("ResolveJobID(known job) = (%q, %v, %v), want (\"job-1\", true, nil)", id, scoped, err)
	}
}

func TestResolveJobID_NilResolverIsUnscoped(t *testing.T) {
	id, scoped, err := hookpacks.ResolveJobID(context.Background(), hookpacks.CompletionHookPayload{JobID: "job-1"}, nil)
	if scoped || id != "" || err != nil {
		t.Fatalf("ResolveJobID(nil resolver) = (%q, %v, %v), want (\"\", false, nil)", id, scoped, err)
	}
}
