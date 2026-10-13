package claude

// Purpose: proves InboxHydration, the PRIMARY path (P1-E37-W8-S73-T1): a
// record reaches only its own session, exactly once, with the provenance
// tag, derived data class and R-16.5 metadata; an expired record is dropped
// at drain; the queue is bounded; a store failure is typed; and a real
// SQLite store keeps pending records across a close/reopen (a restart).

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

func TestInboxHydrationPrimaryPath(t *testing.T) {
	ctx := context.Background()
	now := fixedNow
	h, err := NewInboxHydration(storetest.NewMemStore(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ev := InboxEvent{
		ID: "h1", Class: "addressed", OriginScope: "repo-a", TargetScope: "repo-a", TargetSession: "s1",
		TargetTask: "task-9", Visibility: "private", CorrelationID: "run-7",
		ExpiresAt: now.Add(time.Hour), Body: []byte("SESSION-ONE-ONLY payload"),
	}
	mustEnqueue(t, h, "s1", ev)
	mustEnqueue(t, h, "s1", ev) // a redelivered id is kept once
	mustEnqueue(t, h, "s2", InboxEvent{ID: "h2", Class: "global-critical", Visibility: "shared", Body: []byte("other")})
	expired := InboxEvent{ID: "h3", Class: "addressed", TargetSession: "s1", ExpiresAt: now.Add(time.Minute)}
	mustEnqueue(t, h, "s1", expired)
	now = now.Add(2 * time.Minute)

	recs, res, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || !res.Appended || res.SessionID != "s1" || len(recs) != 1 {
		t.Fatalf("drain s1 = (%d records, %+v, %v), want the one unexpired record", len(recs), res, err)
	}
	want := InboxRecord{
		Envelope:    Envelope{ID: "h1", Class: "addressed", OriginScope: "repo-a", DataClass: "restricted", Summary: recs[0].Summary},
		TargetScope: "repo-a", TargetSession: "s1", TargetTask: "task-9", Visibility: "private", CorrelationID: "run-7",
		ExpiresAtMs: ev.ExpiresAt.UnixMilli(), Provenance: ProvenanceUntrusted,
	}
	if recs[0] != want || strings.Contains(recs[0].Summary, "SESSION-ONE-ONLY payload") {
		t.Fatalf("record = %+v, want %+v with a summary-only body", recs[0], want)
	}
	if again, res, _ := h.AppendNextTurn(ctx, "s1"); len(again) != 0 || res.Appended {
		t.Fatalf("a second drain returned %d records (Appended=%v)", len(again), res.Appended)
	}
	other, _, _ := h.AppendNextTurn(ctx, "s2")
	if len(other) != 1 || other[0].ID != "h2" {
		t.Fatalf("session s2 got %+v", other)
	}
	if _, _, err := h.AppendNextTurn(ctx, ""); err != ErrHydrationTarget {
		t.Fatalf("empty session drain err = %v", err)
	}
}

func mustEnqueue(t *testing.T, h *InboxHydration, sid string, ev InboxEvent) {
	t.Helper()
	if err := h.Enqueue(context.Background(), sid, ev); err != nil {
		t.Fatalf("Enqueue(%s, %s): %v", sid, ev.ID, err)
	}
}

func TestInboxHydrationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cascade.db")
	first, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := NewInboxHydration(first, func() time.Time { return fixedNow })
	mustEnqueue(t, h, "s1", InboxEvent{ID: "durable-1", Class: "addressed", TargetSession: "s1", Body: []byte("kept")})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	restarted, _ := NewInboxHydration(second, func() time.Time { return fixedNow })
	recs, res, err := restarted.AppendNextTurn(ctx, "s1")
	if err != nil || !res.Appended || len(recs) != 1 || recs[0].ID != "durable-1" {
		t.Fatalf("after reopen: (%+v, %+v, %v), want durable-1", recs, res, err)
	}
}

// brokenStore fails every transaction with an untyped error.
type brokenStore struct{ provider.Store }

func (brokenStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return errors.New("disk I/O error")
}

func TestInboxHydrationBoundsAndTypedFailures(t *testing.T) {
	ctx := context.Background()
	h, _ := NewInboxHydration(storetest.NewMemStore(), func() time.Time { return fixedNow })
	for i := range maxPendingPerSession {
		mustEnqueue(t, h, "s1", InboxEvent{ID: "q" + strings.Repeat("x", i), Class: "global-critical"})
	}
	wantExactly(t, h.Enqueue(ctx, "s1", InboxEvent{ID: "over", Class: "global-critical"}), ErrHydrationFull, "queue is full")
	broken, _ := NewInboxHydration(brokenStore{}, func() time.Time { return fixedNow })
	err := broken.Enqueue(ctx, "s1", InboxEvent{ID: "b"})
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("a store failure surfaced as %v", err)
	}
	if _, _, err := broken.AppendNextTurn(ctx, "s1"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a failed drain surfaced as %v", err)
	}
	if _, err := NewInboxHydration(nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewInboxHydration(nil) err = %v", err)
	}
	corrupt := storetest.NewMemStore()
	_ = corrupt.Put(ctx, HydrationNamespace, "s1", []byte("{not json"))
	bad, _ := NewInboxHydration(corrupt, func() time.Time { return fixedNow })
	if _, _, err := bad.AppendNextTurn(ctx, "s1"); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("a corrupt queue surfaced as %v", err)
	}
	if err := bad.Enqueue(ctx, "s1", InboxEvent{ID: "after-corrupt"}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("an enqueue over a corrupt queue surfaced as %v", err)
	}
	if RenderInboxBlock(nil) != "" {
		t.Fatal("an empty drain rendered a block")
	}
}

func TestHydratedRecordCarriesRoutingMetadata(t *testing.T) {
	ctx := context.Background()
	h, _ := NewInboxHydration(storetest.NewMemStore(), func() time.Time { return fixedNow })
	expiry := fixedNow.Add(time.Hour)
	mustEnqueue(t, h, "s1", InboxEvent{
		ID: "meta-1", Class: "addressed", OriginScope: "repo-a", TargetScope: "repo-b", TargetSession: "s1",
		TargetTask: "task-4", Visibility: "scoped", CorrelationID: "corr-9", ExpiresAt: expiry,
		DataClass: provider.SensitivityInternal, Trust: provider.ProvenanceTrusted, Body: []byte("routing body"),
	})
	mustEnqueue(t, h, "s1", InboxEvent{
		ID: "meta-2", Class: "addressed", OriginScope: "repo-a", TargetScope: "repo-a", TargetSession: "s1",
		TargetTask: "task-5", Visibility: "private", CorrelationID: "corr-10", ExpiresAt: expiry, Body: []byte("second"),
	})
	recs, _, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || len(recs) != 2 {
		t.Fatalf("drain = (%d records, %v), want 2", len(recs), err)
	}
	for _, r := range recs {
		for name, v := range map[string]string{
			"origin scope": r.OriginScope, "target scope": r.TargetScope, "target session": r.TargetSession,
			"target task": r.TargetTask, "visibility": r.Visibility, "correlation id": r.CorrelationID,
			"data class": r.DataClass, "provenance": r.Provenance,
		} {
			if v == "" {
				t.Errorf("record %s lacks %s: %+v", r.ID, name, r)
			}
		}
		if r.ExpiresAtMs != expiry.UnixMilli() {
			t.Errorf("record %s expiry = %d, want %d", r.ID, r.ExpiresAtMs, expiry.UnixMilli())
		}
		if r.Provenance != "untrusted-source" {
			t.Errorf("record %s provenance = %q: a producer's Trust tag must never mark hydrated text trusted", r.ID, r.Provenance)
		}
	}
	// The ported data class is the producer's, carried and never lowered.
	if recs[0].DataClass != "internal" || recs[1].DataClass != "restricted" {
		t.Fatalf("data classes = %q, %q; want the events' own (internal, restricted)", recs[0].DataClass, recs[1].DataClass)
	}
	if recs[0].TargetScope != "repo-b" || recs[0].TargetTask != "task-4" || recs[0].CorrelationID != "corr-9" {
		t.Fatalf("record 1 routing metadata drifted: %+v", recs[0])
	}
}

func TestAppendNextTurnReportsResult(t *testing.T) {
	ctx := context.Background()
	now := fixedNow
	h, _ := NewInboxHydration(storetest.NewMemStore(), func() time.Time { return now })
	recs, res, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || res.Appended || res.SessionID != "s1" || len(recs) != 0 {
		t.Fatalf("empty queue = (%d, %+v, %v), want (0, {s1 false}, nil)", len(recs), res, err)
	}
	mustEnqueue(t, h, "s1", InboxEvent{ID: "r1", Class: "addressed", TargetSession: "s1", ExpiresAt: now.Add(time.Minute)})
	now = now.Add(time.Hour)
	recs, res, err = h.AppendNextTurn(ctx, "s1")
	if err != nil || res.Appended || len(recs) != 0 {
		t.Fatalf("all-expired queue = (%d, %+v, %v): Appended must be false when nothing is returned", len(recs), res, err)
	}
	mustEnqueue(t, h, "s1", InboxEvent{ID: "r2", Class: "addressed", TargetSession: "s1"})
	recs, res, err = h.AppendNextTurn(ctx, "s1")
	if err != nil || !res.Appended || res.SessionID != "s1" || len(recs) != 1 {
		t.Fatalf("one live record = (%d, %+v, %v), want (1, {s1 true}, nil)", len(recs), res, err)
	}
	// The drain cleared the queue: a second drain returns nothing.
	if again, res, err := h.AppendNextTurn(ctx, "s1"); err != nil || res.Appended || len(again) != 0 {
		t.Fatalf("second drain = (%d, %+v, %v): the queue was not cleared", len(again), res, err)
	}
}

// recordingStore records every namespace a transaction touches.
type recordingStore struct {
	provider.Store
	mu         sync.Mutex
	namespaces map[string]bool
}

type recordingTx struct {
	provider.Tx
	s *recordingStore
}

func (s *recordingStore) note(namespace string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.namespaces[namespace] = true
}

func (s *recordingStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, recordingTx{Tx: tx, s: s}) })
}

func (t recordingTx) Get(ctx context.Context, ns, key string) ([]byte, error) {
	t.s.note(ns)
	return t.Tx.Get(ctx, ns, key)
}

func (t recordingTx) Delete(ctx context.Context, ns, key string) error {
	t.s.note(ns)
	return t.Tx.Delete(ctx, ns, key)
}

func (t recordingTx) CompareAndSwap(ctx context.Context, ns, key string, old, val []byte) error {
	t.s.note(ns)
	return t.Tx.CompareAndSwap(ctx, ns, key, old, val)
}

func (t recordingTx) Put(ctx context.Context, ns, key string, val []byte) error {
	t.s.note(ns)
	return t.Tx.Put(ctx, ns, key, val)
}

// TestAppendNextTurnNeverAcks proves the drain has no acknowledgement path:
// it reads and clears only its own queue namespace, so the source inbox
// record (another namespace, S-73.T2's to acknowledge) stays unread. The
// real notify inbox is checked in internal/plugins.
func TestAppendNextTurnNeverAcks(t *testing.T) {
	ctx := context.Background()
	rec := &recordingStore{Store: storetest.NewMemStore(), namespaces: map[string]bool{}}
	h, _ := NewInboxHydration(rec, func() time.Time { return fixedNow })
	const inboxNS = "notify.inbox"
	if err := rec.Put(ctx, inboxNS, "unread-1", []byte(`{"read":false}`)); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, h, "s1", InboxEvent{ID: "unread-1", Class: "addressed", TargetSession: "s1"})
	if recs, res, err := h.AppendNextTurn(ctx, "s1"); err != nil || !res.Appended || len(recs) != 1 {
		t.Fatalf("drain = (%d, %+v, %v)", len(recs), res, err)
	}
	if len(rec.namespaces) != 1 || !rec.namespaces[HydrationNamespace] {
		t.Fatalf("the drain touched namespaces %v, want only %q", rec.namespaces, HydrationNamespace)
	}
	if got, err := rec.Get(ctx, inboxNS, "unread-1"); err != nil || string(got) != `{"read":false}` {
		t.Fatalf("the source inbox record changed: (%q, %v)", got, err)
	}
}

// failingDeleteStore fails the delete the drain uses to clear the queue.
type failingDeleteStore struct{ provider.Store }

type failingDeleteTx struct{ provider.Tx }

func (s failingDeleteStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, failingDeleteTx{Tx: tx}) })
}

func (failingDeleteTx) Delete(context.Context, string, string) error { return errors.New("disk full") }

func TestHydrationAppendFailureKeepsInboxRecord(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	h, _ := NewInboxHydration(mem, func() time.Time { return fixedNow })
	mustEnqueue(t, h, "s1", InboxEvent{ID: "keep-1", Class: "addressed", TargetSession: "s1", Body: []byte("still queued")})
	broken, _ := NewInboxHydration(failingDeleteStore{Store: mem}, func() time.Time { return fixedNow })
	recs, res, err := broken.AppendNextTurn(ctx, "s1")
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("a failed clear surfaced as %v, want a typed KindUnavailable naming the cause", err)
	}
	if len(recs) != 0 || res.Appended || res.SessionID != "s1" {
		t.Fatalf("a failed drain reported records: (%d, %+v)", len(recs), res)
	}
	again, res, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || !res.Appended || len(again) != 1 || again[0].ID != "keep-1" {
		t.Fatalf("after the failed drain the record is gone: (%+v, %+v, %v)", again, res, err)
	}
}
