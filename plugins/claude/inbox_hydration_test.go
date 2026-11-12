package claude

// Purpose: proves InboxHydration, the PRIMARY path: a record reaches only
// its own session, exactly once, with the provenance tag, stamped data class
// and R-16.5 metadata; a record above the model ceiling is withheld; an
// expired record is dropped at drain; the queue is bounded; a store failure
// is typed. The restart and class/trust round trip live in
// TestHydrationRecordCarriesClassAndTrust.

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
		ExpiresAt: now.Add(time.Hour), Body: []byte("SESSION-ONE-ONLY payload"), DataClass: provider.SensitivityInternal,
	}
	mustEnqueue(t, h, "s1", ev)
	mustEnqueue(t, h, "s1", ev) // a redelivered id is kept once
	mustEnqueue(t, h, "s2", InboxEvent{ID: "h2", Class: "global-critical", Visibility: "shared", Body: []byte("other"), DataClass: provider.SensitivityPublic})
	expired := InboxEvent{ID: "h3", Class: "addressed", TargetSession: "s1", ExpiresAt: now.Add(time.Minute)}
	mustEnqueue(t, h, "s1", expired)
	now = now.Add(2 * time.Minute)

	recs, res, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || !res.Appended || res.SessionID != "s1" || len(recs) != 1 {
		t.Fatalf("drain s1 = (%d records, %+v, %v), want the one unexpired record", len(recs), res, err)
	}
	want := InboxRecord{
		Envelope: Envelope{ID: "h1", Class: "addressed", OriginScope: "repo-a", DataClass: "internal",
			Trust: "untrusted-source", Summary: recs[0].Summary},
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
		DataClass: provider.SensitivityPublic,
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
	// The ported data class is the stamped one, carried and never lowered.
	if recs[0].DataClass != "internal" || recs[1].DataClass != "public" {
		t.Fatalf("data classes = %q, %q; want the events' own (internal, public)", recs[0].DataClass, recs[1].DataClass)
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
	mustEnqueue(t, h, "s1", InboxEvent{ID: "r1", Class: "addressed", TargetSession: "s1", ExpiresAt: now.Add(time.Minute), DataClass: provider.SensitivityInternal})
	now = now.Add(time.Hour)
	recs, res, err = h.AppendNextTurn(ctx, "s1")
	if err != nil || res.Appended || len(recs) != 0 {
		t.Fatalf("all-expired queue = (%d, %+v, %v): Appended must be false when nothing is returned", len(recs), res, err)
	}
	mustEnqueue(t, h, "s1", InboxEvent{ID: "r2", Class: "addressed", TargetSession: "s1", DataClass: provider.SensitivityInternal})
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

// note records namespace and returns it, so each Tx method stays one line.
func (s *recordingStore) note(namespace string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.namespaces[namespace] = true
	return namespace
}

func (s *recordingStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, recordingTx{Tx: tx, s: s}) })
}

func (t recordingTx) Get(ctx context.Context, ns, key string) ([]byte, error) {
	return t.Tx.Get(ctx, t.s.note(ns), key)
}

func (t recordingTx) Delete(ctx context.Context, ns, key string) error {
	return t.Tx.Delete(ctx, t.s.note(ns), key)
}

func (t recordingTx) CompareAndSwap(ctx context.Context, ns, key string, old, val []byte) error {
	return t.Tx.CompareAndSwap(ctx, t.s.note(ns), key, old, val)
}

func (t recordingTx) Put(ctx context.Context, ns, key string, val []byte) error {
	return t.Tx.Put(ctx, t.s.note(ns), key, val)
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
	mustEnqueue(t, h, "s1", InboxEvent{ID: "keep-1", Class: "addressed", TargetSession: "s1", Body: []byte("still queued"), DataClass: provider.SensitivityInternal})
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

// TestHydrationWithholdsLocalOnly: on a real SQLite store, local-only and
// restricted records never reach the turn, are counted in Withheld and leave
// the queue; internal and public records are returned. The drain has no
// acknowledgement path: it touches only its own queue namespace, so every
// source inbox record (S-73.T2's to acknowledge) stays unread.
func TestHydrationWithholdsLocalOnly(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &recordingStore{Store: db, namespaces: map[string]bool{}}
	h, _ := NewInboxHydration(store, func() time.Time { return fixedNow })
	ids := []string{"lo", "rs", "in", "pu"}
	tiers := []provider.SensitivityTier{provider.SensitivityLocalOnly, provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic}
	for i, id := range ids {
		if err := store.Put(ctx, "notify.inbox", id, []byte(`{"read":false}`)); err != nil {
			t.Fatal(err)
		}
		mustEnqueue(t, h, "s1", InboxEvent{ID: id, Class: "addressed", TargetSession: "s1", DataClass: tiers[i]})
	}
	recs, res, err := h.AppendNextTurn(ctx, "s1")
	if err != nil || res.Withheld != 2 || !res.Appended || len(recs) != 2 || recs[0].ID != "in" || recs[1].ID != "pu" {
		t.Fatalf("drain = (%+v, %+v, %v), want in and pu returned and two withheld", recs, res, err)
	}
	if again, res, err := h.AppendNextTurn(ctx, "s1"); err != nil || len(again) != 0 || res.Withheld != 0 {
		t.Fatalf("second drain = (%d, %+v, %v): withheld records stayed queued", len(again), res, err)
	}
	if len(store.namespaces) != 1 || !store.namespaces[HydrationNamespace] {
		t.Fatalf("the drain touched namespaces %v, want only %q", store.namespaces, HydrationNamespace)
	}
	for _, id := range ids {
		if got, err := store.Get(ctx, "notify.inbox", id); err != nil || string(got) != `{"read":false}` {
			t.Fatalf("inbox record %s changed: (%q, %v)", id, got, err)
		}
	}
	mustEnqueue(t, h, "s1", InboxEvent{ID: "only-lo", Class: "addressed", DataClass: provider.SensitivityLocalOnly})
	if recs, res, err := h.AppendNextTurn(ctx, "s1"); err != nil || len(recs) != 0 || res.Appended || res.Withheld != 1 {
		t.Fatalf("all-withheld drain = (%d, %+v, %v), want nothing appended and one withheld", len(recs), res, err)
	}
}
