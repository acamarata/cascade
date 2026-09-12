package fleet

import (
	"context"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fetchFakeTicker is a manually-driven runtime.Ticker, mirroring the
// governor package's own test fakeTicker (sampler_test.go) — this file
// cannot reuse that unexported type across packages, so it is
// re-declared here at the same, already-established shape.
type fetchFakeTicker struct {
	c        chan struct{}
	stopOnce sync.Once
}

func newFetchFakeTicker() *fetchFakeTicker { return &fetchFakeTicker{c: make(chan struct{})} }

func (f *fetchFakeTicker) C() <-chan struct{} { return f.c }
func (f *fetchFakeTicker) Stop()              { f.stopOnce.Do(func() {}) }

func (f *fetchFakeTicker) Tick(ctx context.Context) {
	select {
	case f.c <- struct{}{}:
	case <-ctx.Done():
	}
}

type fakeSessionLister struct {
	rows []TopSessionRow
	err  error
}

func (f fakeSessionLister) List(context.Context) ([]TopSessionRow, error) {
	return f.rows, f.err
}

type fakeGovernorReader struct{ panel GovernorPanel }

func (f fakeGovernorReader) Snapshot() GovernorPanel { return f.panel }

type fakeTaskReader struct{ panel TaskPanel }

func (f fakeTaskReader) Snapshot() TaskPanel { return f.panel }

func TestFetcher_Fetch_Empty(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(1000, 0))
	f := &Fetcher{Sessions: fakeSessionLister{}, Clock: clk}
	snap, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Sessions) != 0 || snap.Governor.Available || snap.Task.Available {
		t.Fatalf("expected empty snapshot, got %+v", snap)
	}
	if !snap.GeneratedAt.Equal(clk.Now()) {
		t.Fatalf("GeneratedAt = %v, want %v", snap.GeneratedAt, clk.Now())
	}
}

func TestFetcher_Fetch_Partial(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(2000, 0))
	f := &Fetcher{
		Sessions: fakeSessionLister{rows: []TopSessionRow{{SessionID: "s1", State: "running"}}},
		Governor: fakeGovernorReader{panel: GovernorPanel{Available: true, CPUFraction: 0.42}},
		Clock:    clk,
	}
	snap, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Sessions) != 1 || !snap.Governor.Available || snap.Task.Available {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Summary.SessionCount != 1 {
		t.Fatalf("SessionCount = %d, want 1", snap.Summary.SessionCount)
	}
}

func TestFetcher_Fetch_Full(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(3000, 0))
	f := &Fetcher{
		Sessions: fakeSessionLister{rows: []TopSessionRow{
			{SessionID: "s1", State: "running", Lane: "a"},
			{SessionID: "s2", State: "stalled", Lane: "b"},
		}},
		Governor: fakeGovernorReader{panel: GovernorPanel{Available: true, QueueDepth: 3}},
		Task:     fakeTaskReader{panel: TaskPanel{Available: true, TicketID: "P1-X"}},
		Clock:    clk,
	}
	snap, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Summary != (Summary{SessionCount: 2, LanesBusy: 2, StalledCount: 1}) {
		t.Fatalf("Summary = %+v", snap.Summary)
	}
	if snap.Governor.QueueDepth != 3 || snap.Task.TicketID != "P1-X" {
		t.Fatalf("panels not populated: %+v", snap)
	}
}

func TestFetcher_Fetch_SessionListError(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "no daemon")
	f := &Fetcher{Sessions: fakeSessionLister{err: wantErr}, Clock: runtime.NewFixedClock(time.Unix(0, 0))}
	_, err := f.Fetch(context.Background())
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestDecodeSessionEvent_Valid(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(100, 0))
	data := []byte(`{"session_id":"s1","harness":"claude","account":"a1","state":"running","updated_at":40}`)
	row, ok := DecodeSessionEvent(data, clk)
	if !ok {
		t.Fatalf("DecodeSessionEvent: not ok")
	}
	if row.SessionID != "s1" || row.Harness != "claude" || row.State != "running" || row.Elapsed != "1m0s" {
		t.Fatalf("row = %+v", row)
	}
}

func TestDecodeSessionEvent_MalformedNeverPanics(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("not json"),
		[]byte(`{"session_id":""}`),
		[]byte(`{"session_id":123}`),
	}
	for _, c := range cases {
		if _, ok := DecodeSessionEvent(c, clk); ok {
			t.Fatalf("expected ok=false for %q", c)
		}
	}
}

func TestApplySessionEvent_UpsertsAndRecomputesSummary(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(500, 0))
	snap := NewTopSnapshot([]TopSessionRow{{SessionID: "s1", State: "running"}}, GovernorPanel{}, TaskPanel{}, clk.Now())
	updated := ApplySessionEvent(snap, TopSessionRow{SessionID: "s2", State: "stalled"}, clk)
	if len(updated.Sessions) != 2 {
		t.Fatalf("len = %d, want 2", len(updated.Sessions))
	}
	if updated.Summary.StalledCount != 1 {
		t.Fatalf("StalledCount = %d, want 1", updated.Summary.StalledCount)
	}
	// original snapshot's slice must be untouched.
	if len(snap.Sessions) != 1 {
		t.Fatalf("original snapshot mutated")
	}
}

func TestSamplerGovernorReader_NoSampler(t *testing.T) {
	r := SamplerGovernorReader{}
	if r.Snapshot().Available {
		t.Fatalf("expected unavailable with nil sampler")
	}
}

func TestUnavailableTaskReader(t *testing.T) {
	panel := UnavailableTaskReader{}.Snapshot()
	if panel.Available || panel.Unavailable == "" {
		t.Fatalf("expected disclosed unavailable panel, got %+v", panel)
	}
}

// TestSamplerGovernorReader_RealSamplerTick drives a real
// *governor.Sampler through one deterministic tick (a fake Ticker, never
// a real sleep) and proves SamplerGovernorReader reports it as
// Available with the sampled fields once the tick has landed.
func TestSamplerGovernorReader_RealSamplerTick(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("governor.collectMetrics returns ErrUnsupportedPlatform on windows tier-2")
	}
	clk := runtime.NewFixedClock(time.Unix(42, 0))
	ticker := newFetchFakeTicker()
	sampler := governor.NewSampler(governor.SamplerConfig{}, clk, ticker, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sampler.Start(ctx)
	ticker.Tick(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !sampler.Snapshot().SampledAt.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	sampler.Stop()

	reader := SamplerGovernorReader{Sampler: sampler}
	panel := reader.Snapshot()
	if !panel.Available {
		t.Fatalf("expected Available after a real tick, got %+v", panel)
	}
	if panel.MemTotalBytes == 0 {
		t.Fatalf("expected a real MemTotalBytes reading, got 0")
	}
}
