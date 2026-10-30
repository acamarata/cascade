package memory

import (
	"context"
	"testing"
)

// writeKindEntry writes one record of kind k into scope "local".
func writeKindEntry(t *testing.T, f *projectionFixture, name string, k MemoryKind) {
	t.Helper()
	e := validEntry()
	e.Name, e.Kind, e.Body, e.ScopeRef = name, k, "heron", DefaultScopeRef
	if err := f.files.Write(context.Background(), e); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// TestSearchInScopeKinds_NarrowsBeforeLimit: the kind filter runs before
// the cap, so two project rows ahead in id order never crowd the one user
// row out of a limit of 1; an empty kind list matches nothing.
func TestSearchInScopeKinds_NarrowsBeforeLimit(t *testing.T) {
	f := newProjection(t)
	writeKindEntry(t, f, "a1", KindProject)
	writeKindEntry(t, f, "a2", KindProject)
	writeKindEntry(t, f, "z9", KindUser)
	mustRun(t, f)
	ctx := context.Background()
	got, err := f.job.SearchInScopeKindsIncludingExpired(ctx, "heron", DefaultScopeRef, []MemoryKind{KindUser}, 1)
	if err != nil || len(got) != 1 || got[0].ID != "user/z9" {
		t.Fatalf("kinds [user] limit 1 = %+v err %v, want exactly user/z9", got, err)
	}
	none, err := f.job.SearchInScopeKindsIncludingExpired(ctx, "heron", DefaultScopeRef, nil, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty kind list = %+v err %v, want nothing", none, err)
	}
}
