package supervision

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sequentialIDGenerator returns "id-001", "id-002", ... deterministically,
// so tests can assert on exact IDs without depending on crypto/rand.
func sequentialIDGenerator() IDGenerator {
	var n atomic.Int64
	return func() string {
		return fmt.Sprintf("id-%03d", n.Add(1))
	}
}

func newTestStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(now)
	return NewStore(kv, clock, nil, sequentialIDGenerator(), 0)
}

func sessionScope(id string) ScopeRef {
	return ScopeRef{Kind: scope.ScopeKindSession, ID: id}
}

func TestStorePushGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, now)

	item := AttentionItem{Kind: KindStall, SourceRef: "sess-1", ScopeRef: sessionScope("sess-1"), Priority: 3}
	pushed, err := s.Push(ctx, item)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if pushed.ID == "" {
		t.Fatal("Push did not mint an ID")
	}
	if pushed.CreatedAt != now.UnixMilli() {
		t.Errorf("CreatedAt = %d, want %d", pushed.CreatedAt, now.UnixMilli())
	}

	got, err := s.Get(ctx, pushed.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != pushed {
		t.Errorf("Get() = %+v, want %+v", got, pushed)
	}
}

func TestStorePushIdempotentOnKindSourceRef(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	item := AttentionItem{Kind: KindStall, SourceRef: "sess-1", ScopeRef: sessionScope("sess-1")}

	first, err := s.Push(ctx, item)
	if err != nil {
		t.Fatalf("first Push: %v", err)
	}
	second, err := s.Push(ctx, item)
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("duplicate push minted a new ID: first=%s second=%s", first.ID, second.ID)
	}

	items, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("len(items) = %d, want 1 (idempotent push must not create a second row)", len(items))
	}
}

func TestStoreGetMissingIDReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	_, err := s.Get(ctx, "does-not-exist")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get(missing) err = %v, want KindNotFound", err)
	}
}

func TestStoreAckAlreadyAckedReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	item := AttentionItem{Kind: KindStall, SourceRef: "sess-1", ScopeRef: sessionScope("sess-1")}
	pushed, err := s.Push(ctx, item)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := s.Ack(ctx, pushed.ID); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	_, err = s.Ack(ctx, pushed.ID)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("second Ack err = %v, want KindNotFound", err)
	}
}

func TestStoreAckExcludesFromDefaultList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	item := AttentionItem{Kind: KindStall, SourceRef: "sess-1", ScopeRef: sessionScope("sess-1")}
	pushed, err := s.Push(ctx, item)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	before, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{})
	if err != nil || len(before) != 1 {
		t.Fatalf("before ack: items=%v err=%v, want 1 item", before, err)
	}

	if _, err := s.Ack(ctx, pushed.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	after, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("after ack, default list still returns %d items, want 0", len(after))
	}

	withAcked, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{IncludeAcked: true})
	if err != nil || len(withAcked) != 1 {
		t.Fatalf("include_acked list: items=%v err=%v, want 1 item", withAcked, err)
	}
}

func TestStoreListKindFilterReturnsOnlyMatching(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	items := []AttentionItem{
		{Kind: KindStall, SourceRef: "a", ScopeRef: sessionScope("s1")},
		{Kind: KindError, SourceRef: "b", ScopeRef: sessionScope("s1")},
		{Kind: KindStall, SourceRef: "c", ScopeRef: sessionScope("s1")},
	}
	for _, it := range items {
		if _, err := s.Push(ctx, it); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	stall := KindStall
	got, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("s1")}, Filter{KindFilter: &stall})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, it := range got {
		if it.Kind != KindStall {
			t.Errorf("got item with Kind=%s, want only KindStall", it.Kind)
		}
	}
}

func TestStoreListOrderingPriorityThenCreatedAtThenID(t *testing.T) {
	ctx := context.Background()
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := NewStore(kv, clock, nil, sequentialIDGenerator(), 0)

	// Two items at the SAME priority and the SAME clock instant (no
	// clock advance between pushes): ID is the only remaining tiebreak.
	low, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "low-pri", ScopeRef: sessionScope("s1"), Priority: 5})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	high, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "high-pri", ScopeRef: sessionScope("s1"), Priority: 1})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	tie1, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "tie-a", ScopeRef: sessionScope("s1"), Priority: 5})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	got, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("s1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[0].ID != high.ID {
		t.Errorf("got[0] = %s, want the priority-1 item %s first", got[0].ID, high.ID)
	}
	// low and tie1 share priority 5 and CreatedAt; ID order breaks the tie.
	wantSecond, wantThird := low.ID, tie1.ID
	if wantSecond > wantThird {
		wantSecond, wantThird = wantThird, wantSecond
	}
	if got[1].ID != wantSecond || got[2].ID != wantThird {
		t.Errorf("tie order = [%s, %s], want [%s, %s]", got[1].ID, got[2].ID, wantSecond, wantThird)
	}
}

func TestStoreListNeverCrossesScope(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now())
	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "in-scope", ScopeRef: sessionScope("s1")}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "other-scope", ScopeRef: sessionScope("s2")}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := s.ListInScopes(ctx, []ScopeRef{sessionScope("s1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(got) != 1 || got[0].SourceRef != "in-scope" {
		t.Errorf("ListInScopes(s1) = %+v, want exactly the in-scope item", got)
	}
}

func TestStoreEvictionRefusesWhenFullAndNothingAcked(t *testing.T) {
	ctx := context.Background()
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Now())
	s := NewStore(kv, clock, nil, sequentialIDGenerator(), 2)

	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "a", ScopeRef: sessionScope("s1")}); err != nil {
		t.Fatalf("Push a: %v", err)
	}
	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "b", ScopeRef: sessionScope("s1")}); err != nil {
		t.Fatalf("Push b: %v", err)
	}
	_, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "c", ScopeRef: sessionScope("s1")})
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("third Push err = %v, want KindQuotaExhausted (queue full, nothing evictable)", err)
	}
}

func TestStoreEvictionReclaimsOldestAcked(t *testing.T) {
	ctx := context.Background()
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Now())
	s := NewStore(kv, clock, nil, sequentialIDGenerator(), 2)

	a, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "a", ScopeRef: sessionScope("s1")})
	if err != nil {
		t.Fatalf("Push a: %v", err)
	}
	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "b", ScopeRef: sessionScope("s1")}); err != nil {
		t.Fatalf("Push b: %v", err)
	}
	if _, err := s.Ack(ctx, a.ID); err != nil {
		t.Fatalf("Ack a: %v", err)
	}

	c, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "c", ScopeRef: sessionScope("s1")})
	if err != nil {
		t.Fatalf("Push c after evicting acked a: %v", err)
	}
	if _, err := s.Get(ctx, a.ID); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("evicted item a still Get()-able: err=%v", err)
	}
	if _, err := s.Get(ctx, c.ID); err != nil {
		t.Errorf("newly pushed item c not found: %v", err)
	}
}
