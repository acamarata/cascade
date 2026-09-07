// Purpose: Store's required unit tests (Art.2): all five ops on a real
//
//	storetest.MemStore, plus not-found, invalid-transition, idempotent
//	same-state re-transition, Touch-unknown-session, and
//	Touch-unknown-field. See domain_isolation_test.go (R-14.117
//	authorized split, Art.10.3's 300-line cap) for the real-driver
//	domain-isolation and event-emission tests.
//
// SPORT: internal.fleet.sessions.Store/ADDED (P1-E12-W3-S24-T3).
package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func newTestStore(t *testing.T) (*sessions.Store, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	return sessions.New(storetest.NewMemStore(), clock, nil), clock
}

func ptr(s string) *string { return &s }

func TestSessionDomain_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestStore(t)

	rec := sessions.SessionRecord{SessionID: "s1", Harness: "claude", Account: "a1", PID: 123, State: "starting", StartedAt: clock.Now().UnixMilli()}
	if err := store.Upsert(ctx, rec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Harness != "claude" || got.PID != 123 || got.State != "starting" {
		t.Fatalf("Get = %+v, want harness=claude pid=123 state=starting", got)
	}
	if got.UpdatedAt != clock.Now().UnixMilli() {
		t.Fatalf("UpdatedAt = %d, want stamped from clock %d", got.UpdatedAt, clock.Now().UnixMilli())
	}
}

func TestSessionDomain_Get_EmptyID(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.Get(context.Background(), "")
	if !errors.Is(err, sessions.ErrInvalidRecord) {
		t.Fatalf("Get(\"\") = %v, want ErrInvalidRecord", err)
	}
}

func TestSessionDomain_Upsert_EmptyID(t *testing.T) {
	store, _ := newTestStore(t)
	err := store.Upsert(context.Background(), sessions.SessionRecord{})
	if !errors.Is(err, sessions.ErrInvalidRecord) {
		t.Fatalf("Upsert with empty session id = %v, want ErrInvalidRecord", err)
	}
}

func TestSessionDomain_List_ParentAndAccountFilters(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	parent := "root-session"
	other := "other-session"
	must := func(rec sessions.SessionRecord) {
		t.Helper()
		if err := store.Upsert(ctx, rec); err != nil {
			t.Fatalf("Upsert %q: %v", rec.SessionID, err)
		}
	}
	must(sessions.SessionRecord{SessionID: "child1", Account: "a1", ParentSessionID: &parent})
	must(sessions.SessionRecord{SessionID: "child2", Account: "a2", ParentSessionID: &other})
	must(sessions.SessionRecord{SessionID: "orphan", Account: "a1"})

	got, err := store.List(ctx, sessions.Filter{Account: ptr("a1")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(account=a1) = %d, want 2: %+v", len(got), got)
	}

	got, err = store.List(ctx, sessions.Filter{ParentSessionID: &parent})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "child1" {
		t.Fatalf("List(parent=root-session) = %+v, want [child1]", got)
	}

	got, err = store.List(ctx, sessions.Filter{ParentSessionID: &parent, Account: ptr("a2")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(parent=root-session,account=a2) = %+v, want none (orphan has no parent)", got)
	}
}

func TestSessionDomain_GetNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.Get(context.Background(), "missing")
	if !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestSessionDomain_List_Filters(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	must := func(rec sessions.SessionRecord) {
		t.Helper()
		if err := store.Upsert(ctx, rec); err != nil {
			t.Fatalf("Upsert %q: %v", rec.SessionID, err)
		}
	}
	must(sessions.SessionRecord{SessionID: "s1", Harness: "claude", Account: "a1", State: "running"})
	must(sessions.SessionRecord{SessionID: "s2", Harness: "codex", Account: "a1", State: "running"})
	must(sessions.SessionRecord{SessionID: "s3", Harness: "claude", Account: "a2", State: "stopped"})

	got, err := store.List(ctx, sessions.Filter{Harness: ptr("claude")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(harness=claude) = %d records, want 2: %+v", len(got), got)
	}

	got, err = store.List(ctx, sessions.Filter{Harness: ptr("claude"), State: ptr("stopped")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "s3" {
		t.Fatalf("List(harness=claude,state=stopped) = %+v, want [s3]", got)
	}
}

func TestSessionDomain_TransitionState_Success(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", State: "starting"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.TransitionState(ctx, "s1", "starting", "running"); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "running" {
		t.Fatalf("State = %q, want running", got.State)
	}
}

func TestSessionDomain_TransitionState_IdempotentSameState(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", State: "running"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.TransitionState(ctx, "s1", "running", "running"); err != nil {
		t.Fatalf("idempotent same-state TransitionState: %v", err)
	}
}

func TestSessionDomain_TransitionState_InvalidTransition(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", State: "starting"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	err := store.TransitionState(ctx, "s1", "running", "stopped")
	if !errors.Is(err, sessions.ErrInvalidTransition) {
		t.Fatalf("TransitionState from mismatched state = %v, want ErrInvalidTransition", err)
	}
}

func TestSessionDomain_TransitionState_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	err := store.TransitionState(context.Background(), "missing", "a", "b")
	if !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("TransitionState on missing session = %v, want ErrNotFound", err)
	}
}

func TestSessionDomain_Touch_TimestampFields(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for _, field := range []string{sessions.FieldInstructionsLoadedAt, sessions.FieldLastPromptAt, sessions.FieldLastToolAt} {
		if err := store.Touch(ctx, "s1", field); err != nil {
			t.Fatalf("Touch(%s): %v", field, err)
		}
	}
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	now := clock.Now().UnixMilli()
	if got.InstructionsLoadedAt == nil || *got.InstructionsLoadedAt != now {
		t.Errorf("InstructionsLoadedAt = %v, want %d", got.InstructionsLoadedAt, now)
	}
	if got.LastPromptAt == nil || *got.LastPromptAt != now {
		t.Errorf("LastPromptAt = %v, want %d", got.LastPromptAt, now)
	}
	if got.LastToolAt == nil || *got.LastToolAt != now {
		t.Errorf("LastToolAt = %v, want %d", got.LastToolAt, now)
	}
}

func TestSessionDomain_Touch_Counters(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Touch(ctx, "s1", sessions.FieldToolCount); err != nil {
		t.Fatalf("Touch(tool_count): %v", err)
	}
	if err := store.Touch(ctx, "s1", sessions.FieldToolCount); err != nil {
		t.Fatalf("Touch(tool_count) again: %v", err)
	}
	if err := store.Touch(ctx, "s1", sessions.FieldCompactionCount); err != nil {
		t.Fatalf("Touch(compaction_count): %v", err)
	}
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2", got.ToolCount)
	}
	if got.CompactionCount != 1 {
		t.Errorf("CompactionCount = %d, want 1", got.CompactionCount)
	}
}

func TestSessionDomain_Touch_UnknownSession(t *testing.T) {
	store, _ := newTestStore(t)
	err := store.Touch(context.Background(), "missing", sessions.FieldToolCount)
	if !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("Touch on missing session = %v, want ErrNotFound", err)
	}
}

func TestSessionDomain_Touch_UnknownField(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	err := store.Touch(ctx, "s1", "cpu_cores")
	if !errors.Is(err, sessions.ErrUnknownField) {
		t.Fatalf("Touch(cpu_cores) = %v, want ErrUnknownField", err)
	}
}

// See domain_isolation_test.go (R-14.117 authorized split, Art.10.3's
// 300-line cap) for domain-isolation and event-emission tests.
