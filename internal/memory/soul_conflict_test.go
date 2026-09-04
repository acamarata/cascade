package memory

// Purpose: what happens AFTER a conflict — that a diverged SOUL is not a
//   dead end (an explicit write resolves it, with the divergence on record
//   first), that a store with no event bus still refuses and still leaves
//   the note, and that a conflict which cannot be reported is never
//   downgraded to a silent success.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestExplicitWriteResolvesAConflict proves a diverged SOUL is not a dead
// end. Route (b) refuses to resolve it, but a person saying what the
// document should be is honoured — and the divergence is on record before
// that write lands, so nothing was accepted or discarded silently.
func TestExplicitWriteResolvesAConflict(t *testing.T) {
	ctx := context.Background()
	f := divergedFixture(t)

	if _, err := f.store.Edit(ctx, SoulDocument{Body: "what I actually want"}); err != nil {
		t.Fatalf("resolving edit: %v", err)
	}
	if len(f.sink.events) != 1 {
		t.Fatalf("the resolving write did not record the conflict: %d events", len(f.sink.events))
	}
	view, err := f.store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Diverged || view.Document.Body != "what I actually want" || view.Version != 2 {
		t.Fatalf("conflict not resolved: %+v", view)
	}
	// The chat route resolves one the same way.
	g := divergedFixture(t)
	if err := g.store.EditViaChat(ctx, SoulDocument{Body: "decided in chat"}); err != nil {
		t.Fatalf("resolving chat edit: %v", err)
	}
	gview, err := g.store.Get(ctx)
	if err != nil || gview.Diverged || gview.Document.Body != "decided in chat" {
		t.Fatalf("chat resolution failed: %+v, %v", gview, err)
	}
}

// TestReconcileWithNoSinkStillRefuses proves the documented no-sink
// configuration: a store built without an event bus discards the
// notification and still leaves the note and still refuses. A conflict
// must not become invisible just because nobody is subscribed.
func TestReconcileWithNoSinkStillRefuses(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	silent := NewFileSoulStore(f.base, f.clock, nil)
	if _, err := silent.Edit(ctx, SoulDocument{Body: "the store's version"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	writeFileAs(t, f.documentPath(), "the editor's version")
	if _, err := silent.DetectDivergence(ctx); !errors.Is(err, ErrSoulDiverged) {
		t.Fatalf("detect = %v, want ErrSoulDiverged", err)
	}
	if _, err := os.Stat(f.notePath()); err != nil {
		t.Fatalf("no note was left for a bus-less store: %v", err)
	}
}

// TestReconcileSurfacesSinkAndNoteFailures proves a conflict that cannot
// be reported is not downgraded to a silent success.
func TestReconcileSurfacesSinkAndNoteFailures(t *testing.T) {
	ctx := context.Background()
	f := divergedFixture(t)
	f.sink.failWith = errors.New("bus refused the event")
	if _, err := f.store.DetectDivergence(ctx); err == nil {
		t.Fatal("a conflict whose event could not be published reported success")
	}

	sys := newFailingFS()
	store := newSoulStoreWithFS(t.TempDir(), newTestClock(), nil, sys)
	if _, err := store.Edit(ctx, SoulDocument{Body: "first"}); err != nil {
		t.Fatalf("seed edit: %v", err)
	}
	sys.present[store.documentPath()] = []byte("edited outside")
	sys.failWrite = true
	if _, err := store.DetectDivergence(ctx); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("note write failure = %v, want ErrStoreIO", err)
	}
}
