// Purpose: contract:session-taint tests (P1-HRN-08): Taint's read shape,
// the sticky MarkUntrusted, not-found identity, fail-closed store errors
// and the taint surviving concurrent writes from two Store values.
package sessions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failingGetStore fails every Get with a non-NotFound error.
type failingGetStore struct{ provider.Store }

func (failingGetStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "disk gone")
}

func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
}

func seed(t *testing.T, s *sessions.Store, id string) {
	t.Helper()
	if err := s.Upsert(context.Background(), sessions.SessionRecord{SessionID: id, Harness: "claude", State: "running"}); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
}

func assertNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound identity", err)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
	if !strings.Contains(err.Error(), sessions.ErrNotFound.Error()) {
		t.Fatalf("err message %q lacks %q", err.Error(), sessions.ErrNotFound.Error())
	}
}

func TestSessionTaintContract(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	store, clock := newTestStore(t)
	seed(t, store, "s1")
	seed(t, store, "clean")

	if err := store.MarkUntrusted(ctx, "s1", "hydrate:abc"); err != nil {
		t.Fatalf("MarkUntrusted: %v", err)
	}
	got, err := store.Taint(ctx, "s1")
	if err != nil {
		t.Fatalf("Taint: %v", err)
	}
	want := sessions.TaintState{Tainted: true, SinceMs: clock.Now().UnixMilli(), ProvenanceRef: "hydrate:abc"}
	if got != want {
		t.Fatalf("Taint = %+v, want %+v", got, want)
	}
	if clean, err := store.Taint(ctx, "clean"); err != nil || clean != (sessions.TaintState{}) {
		t.Fatalf("Taint(clean) = %+v, %v; want zero state, nil", clean, err)
	}

	_, err = store.Taint(ctx, "nope")
	assertNotFound(t, err)

	broken := sessions.New(failingGetStore{storetest.NewMemStore()}, clock, nil)
	st, err := broken.Taint(ctx, "s1")
	if err == nil {
		t.Fatal("Taint on a failing store returned nil error")
	}
	if cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("store error surfaced as NotFound: %v", err)
	}
	if !st.Tainted {
		t.Fatalf("Taint on error = %+v, want Tainted true (fail closed)", st)
	}
}

func TestMarkUntrustedIsSticky(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	store, clock := newTestStore(t)
	seed(t, store, "s1")
	if err := store.MarkUntrusted(ctx, "s1", "first"); err != nil {
		t.Fatalf("MarkUntrusted: %v", err)
	}
	first := clock.Now().UnixMilli()
	clock.Advance(time.Minute)
	if err := store.MarkUntrusted(ctx, "s1", "second"); err != nil {
		t.Fatalf("second MarkUntrusted: %v", err)
	}
	seed(t, store, "s1") // a fresh record with Untrusted false
	if err := store.Touch(ctx, "s1", sessions.FieldToolCount); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	rec, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rec.Untrusted || rec.UntrustedSince == nil || *rec.UntrustedSince != first || rec.UntrustedRef != "first" {
		t.Fatalf("record taint = %v/%v/%q, want true/%d/first", rec.Untrusted, rec.UntrustedSince, rec.UntrustedRef, first)
	}
}

func TestTaintUnknownIsNotFound(t *testing.T) {
	isolateHome(t)
	store, _ := newTestStore(t)
	assertNotFound(t, store.MarkUntrusted(context.Background(), "ghost", "ref"))
	_, err := store.Taint(context.Background(), "ghost")
	assertNotFound(t, err)
}

func TestTaintSurvivesConcurrentWrites(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	mem := storetest.NewMemStore()
	a := sessions.New(mem, clock, nil)
	b := sessions.New(mem, clock, nil)
	seed(t, a, "s1")

	var wg sync.WaitGroup
	errs := make(chan error, 101)
	for i := 0; i < 100; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func(i int, s *sessions.Store) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				errs <- s.Touch(ctx, "s1", sessions.FieldToolCount)
			case 1:
				errs <- s.TransitionState(ctx, "s1", "running", "running")
			default:
				errs <- s.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", Harness: "claude", State: "running"})
			}
		}(i, s)
	}
	wg.Add(1)
	go func() { defer wg.Done(); errs <- b.MarkUntrusted(ctx, "s1", "mark-ref") }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	got, err := a.Taint(ctx, "s1")
	if err != nil {
		t.Fatalf("Taint: %v", err)
	}
	want := sessions.TaintState{Tainted: true, SinceMs: clock.Now().UnixMilli(), ProvenanceRef: "mark-ref"}
	if got != want {
		t.Fatalf("Taint = %+v, want %+v", got, want)
	}
}
