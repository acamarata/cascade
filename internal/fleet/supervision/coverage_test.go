package supervision

// Purpose (this file): closes the remaining coverage gaps left by
// attention_store_test.go/routing_test.go/rpc_test.go's happy-path and
// named acceptance tests: EventBus wiring, checkDataClass's real
// allow/deny/error branches, existingForDedup's non-NotFound error leg,
// and the two RPC handler branches (missing own_scope on get, a decode
// error) those files did not already reach.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingBus is an EventBus that records every publish and can be
// configured to error.
type recordingBus struct {
	published int
	err       error
}

func (b *recordingBus) Publish(_ context.Context, _ string, kind events.EventKind, _ string, payload []byte) (events.Event, error) {
	b.published++
	if b.err != nil {
		return events.Event{}, b.err
	}
	return events.Event{Kind: kind, Payload: payload}, nil
}

func TestStorePushAndAckEmitOnRealBus(t *testing.T) {
	ctx := context.Background()
	bus := &recordingBus{}
	s := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), bus, sequentialIDGenerator(), 0)
	item, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "s1", ScopeRef: sessionScope("s1")})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := s.Ack(ctx, item.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if bus.published != 2 {
		t.Errorf("published = %d, want 2 (one for Push, one for Ack)", bus.published)
	}
}

func TestStoreEmitSwallowsBusError(t *testing.T) {
	ctx := context.Background()
	bus := &recordingBus{err: cascade.New(cascade.KindUnavailable, "boom")}
	s := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), bus, sequentialIDGenerator(), 0)
	if _, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "s1", ScopeRef: sessionScope("s1")}); err != nil {
		t.Fatalf("Push must succeed even when the bus errors (fire-and-forget): %v", err)
	}
}

// dedupErrorStore wraps a real MemStore but returns a non-NotFound error
// from Get on any dedup-prefixed key, to exercise existingForDedup's
// error leg.
type dedupErrorStore struct {
	*storetest.MemStore
}

func (d dedupErrorStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if len(key) >= len(attnDedupPrefix) && key[:len(attnDedupPrefix)] == attnDedupPrefix {
		return nil, cascade.New(cascade.KindUnavailable, "dedupErrorStore: simulated failure")
	}
	return d.MemStore.Get(ctx, namespace, key)
}

func TestStorePushPropagatesDedupLookupError(t *testing.T) {
	ctx := context.Background()
	kv := dedupErrorStore{MemStore: storetest.NewMemStore()}
	s := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	_, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "s1", ScopeRef: sessionScope("s1")})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Push err = %v, want the dedup lookup failure propagated", err)
	}
}

// fakeDataClassChecker is a deterministic DataClassChecker for tests.
type fakeDataClassChecker struct {
	allow bool
	err   error
}

func (f fakeDataClassChecker) Allow(context.Context, AttentionItem) (bool, error) {
	return f.allow, f.err
}

func TestCheckDataClassAllowDenyError(t *testing.T) {
	ctx := context.Background()
	item := AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: sessionScope("s1")}

	if err := checkDataClass(ctx, nil, item); err != nil {
		t.Errorf("nil DataClassChecker should always pass, got %v", err)
	}
	if err := checkDataClass(ctx, fakeDataClassChecker{allow: true}, item); err != nil {
		t.Errorf("allowing checker should pass, got %v", err)
	}
	if err := checkDataClass(ctx, fakeDataClassChecker{allow: false}, item); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("denying checker err = %v, want KindPermissionDenied", err)
	}
	wantErr := cascade.New(cascade.KindUnavailable, "boom")
	if err := checkDataClass(ctx, fakeDataClassChecker{err: wantErr}, item); err != wantErr {
		t.Errorf("erroring checker err = %v, want the underlying error propagated", err)
	}
}

func TestRoutePushWithDataClassCheckerEndToEnd(t *testing.T) {
	ctx := context.Background()
	own := sessionScope("s1")
	kv := storetest.NewMemStore()
	s := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	req := PushRequest{Item: AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: own}, Origin: own}

	if _, err := RoutePush(ctx, s, nil, fakeDataClassChecker{allow: false}, req); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("RoutePush with a denying data-class checker = %v, want KindPermissionDenied", err)
	}
	count, err := s.countAll(ctx)
	if err != nil || count != 0 {
		t.Fatalf("countAll = %d err=%v, want 0 (denied push queues nothing)", count, err)
	}
}

func TestFleetAttentionGetHandlerMissingOwnScopeSkipsVisibilityCheck(t *testing.T) {
	ctx := context.Background()
	s := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	item, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: sessionScope("s1")})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, s, nil)
	var result attnGetResult
	caller := &registryCaller{reg: reg}
	if err := caller.Do(ctx, MethodGet, attnGetParams{ID: item.ID}, &result); err != nil {
		t.Fatalf("Get with no own_scope: %v", err)
	}
	if result.Item.ID != item.ID {
		t.Errorf("result = %+v, want id=%s", result.Item, item.ID)
	}
}

func TestFleetAttentionGetHandlerScopedOutReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	item, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: sessionScope("other-scope")})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, s, nil)
	client := NewClient(&registryCaller{reg: reg})
	_, err = client.Get(ctx, item.ID, sessionScope("caller-scope"), nil)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get across scopes err = %v, want KindNotFound (visibility failure looks like not-found)", err)
	}
}

func TestKindForSessionStateStallMapping(t *testing.T) {
	// sessions.StateStalled is exercised via subscribe_test.go's blocked
	// case for Blocked; this asserts the Stall branch directly.
	if got := kindForSessionState(sessions.StateStalled); got != KindStall {
		t.Errorf("kindForSessionState(Stalled) = %s, want KindStall", got)
	}
}

// evictErrorStore fails Delete on the scope-index key, to exercise
// evict's final error leg.
type evictErrorStore struct {
	*storetest.MemStore
}

func (e evictErrorStore) Delete(ctx context.Context, namespace, key string) error {
	if len(key) >= len(attnScopeIndexPrefix) && key[:len(attnScopeIndexPrefix)] == attnScopeIndexPrefix {
		return cascade.New(cascade.KindUnavailable, "evictErrorStore: simulated failure")
	}
	return e.MemStore.Delete(ctx, namespace, key)
}

func TestStoreEvictionPropagatesDeleteError(t *testing.T) {
	ctx := context.Background()
	kv := evictErrorStore{MemStore: storetest.NewMemStore()}
	s := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 1)
	a, err := s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "a", ScopeRef: sessionScope("s1")})
	if err != nil {
		t.Fatalf("Push a: %v", err)
	}
	if _, err := s.Ack(ctx, a.ID); err != nil {
		t.Fatalf("Ack a: %v", err)
	}
	_, err = s.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "b", ScopeRef: sessionScope("s1")})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Push b (triggering eviction) err = %v, want the propagated delete failure", err)
	}
}

var _ provider.Store = dedupErrorStore{}
var _ provider.Store = evictErrorStore{}
