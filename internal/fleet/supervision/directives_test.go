package supervision

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func newTestDirectives(t *testing.T) *DirectiveStore {
	t.Helper()
	return NewDirectiveStore(storetest.NewMemStore(), runtime.NewFixedClock(stallT0))
}

func mustEnqueue(t *testing.T, s *DirectiveStore, d Directive) {
	t.Helper()
	if err := s.Enqueue(context.Background(), d); err != nil {
		t.Fatalf("Enqueue(%+v): %v", d, err)
	}
}

// TestDirectiveStoreDrainIsExactlyOnce races two Drains over one queue:
// their results must be disjoint and together hold every queued entry.
func TestDirectiveStoreDrainIsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestDirectives(t)
	const queued = 20
	for i := 0; i < queued; i++ {
		mustEnqueue(t, s, Directive{SessionID: "s1", Kind: DirectiveRetry, Text: "t", StalledSince: int64(i + 1)})
	}
	var wg sync.WaitGroup
	results := make([][]Directive, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.Drain(ctx, "s1")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Drain #%d: %v", i, err)
		}
	}
	seen := map[int64]int{}
	for _, r := range results {
		for _, d := range r {
			seen[d.StalledSince]++
		}
	}
	if len(seen) != queued {
		t.Fatalf("union holds %d distinct entries, want %d", len(seen), queued)
	}
	for since, n := range seen {
		if n != 1 {
			t.Errorf("entry %d returned %d times, want exactly once", since, n)
		}
	}
	if rest, err := s.Drain(ctx, "s1"); err != nil || len(rest) != 0 {
		t.Errorf("queue after drains = (%v, %v), want empty", rest, err)
	}
}

func TestDirectiveStoreDedupesPendingEpisode(t *testing.T) {
	ctx := context.Background()
	s := newTestDirectives(t)
	d := Directive{SessionID: "s1", Kind: DirectiveRetry, Text: "t", StalledSince: 100}
	mustEnqueue(t, s, d)
	mustEnqueue(t, s, d)
	mustEnqueue(t, s, Directive{SessionID: "s1", Kind: DirectiveContext, Text: "t", StalledSince: 100})
	mustEnqueue(t, s, Directive{SessionID: "s1", Kind: DirectiveRetry, Text: "t", StalledSince: 200})
	mustEnqueue(t, s, Directive{SessionID: "s2", Kind: DirectiveRetry, Text: "t", StalledSince: 100})
	got, err := s.Drain(ctx, "s1")
	if err != nil || len(got) != 3 {
		t.Fatalf("Drain(s1) = (%v, %v), want 3 distinct directives", got, err)
	}
	// Once drained the episode is no longer pending: it may enqueue again.
	mustEnqueue(t, s, d)
	if again, _ := s.Drain(ctx, "s1"); len(again) != 1 {
		t.Errorf("re-enqueue after drain returned %d, want 1", len(again))
	}
	if other, _ := s.Drain(ctx, "s2"); len(other) != 1 {
		t.Errorf("Drain(s2) = %d, want 1 (sessions are independent)", len(other))
	}
}

func TestDirectiveStoreDrainOrdered(t *testing.T) {
	ctx := context.Background()
	s := newTestDirectives(t)
	const n = 12 // crosses the 9 -> 10 digit boundary a naive key sort would misorder
	for i := 0; i < n; i++ {
		mustEnqueue(t, s, Directive{SessionID: "s1", Kind: DirectiveRetry, Text: fmt.Sprintf("t%d", i), StalledSince: int64(i + 1)})
	}
	got, err := s.Drain(ctx, "s1")
	if err != nil || len(got) != n {
		t.Fatalf("Drain = (%d, %v), want %d", len(got), err, n)
	}
	for i, d := range got {
		if want := fmt.Sprintf("t%d", i); d.Text != want {
			t.Fatalf("entry %d = %q, want %q (enqueue order)", i, d.Text, want)
		}
	}
}

// txFailStore delegates everything but fails every transaction.
type txFailStore struct{ provider.Store }

var errTxDown = cascade.New(cascade.KindUnavailable, "tx down")

func (txFailStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return errTxDown
}

func TestDirectiveStoreDrainErrorRemovesNothing(t *testing.T) {
	ctx := context.Background()
	kv := storetest.NewMemStore()
	healthy := NewDirectiveStore(kv, runtime.NewFixedClock(stallT0))
	mustEnqueue(t, healthy, Directive{SessionID: "s1", Kind: DirectiveRetry, Text: "t", StalledSince: 1})
	broken := NewDirectiveStore(txFailStore{kv}, runtime.NewFixedClock(stallT0))
	got, err := broken.Drain(ctx, "s1")
	if err != errTxDown || got != nil {
		t.Fatalf("Drain on a failing store = (%v, %v), want (nil, errTxDown)", got, err)
	}
	if left, err := healthy.Drain(ctx, "s1"); err != nil || len(left) != 1 {
		t.Fatalf("after the failed Drain the queue holds (%v, %v), want the one entry intact", left, err)
	}
}

func TestDirectiveStoreRefusesBadInputTyped(t *testing.T) {
	ctx := context.Background()
	s := newTestDirectives(t)
	for name, d := range map[string]Directive{
		"no session": {Kind: DirectiveRetry, Text: "t"},
		"no text":    {SessionID: "s1", Kind: DirectiveRetry},
		"bad kind":   {SessionID: "s1", Kind: "other", Text: "t"},
	} {
		if err := s.Enqueue(ctx, d); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: Enqueue err = %v, want InvalidInput", name, err)
		}
	}
	if _, err := s.Drain(ctx, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Drain(\"\") err = %v, want InvalidInput", err)
	}
	var nilStore *DirectiveStore
	if err := nilStore.Enqueue(ctx, Directive{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil store Enqueue err = %v, want InvalidInput", err)
	}
}
