package governor

import (
	"os"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sampleOnceTicker is a manually driven runtime.Ticker: C never fires
// until the test sends on it, and Stop is recorded.
type sampleOnceTicker struct {
	c       chan struct{}
	mu      sync.Mutex
	stopped bool
}

func newSampleOnceTicker() *sampleOnceTicker { return &sampleOnceTicker{c: make(chan struct{})} }

func (f *sampleOnceTicker) C() <-chan struct{} { return f.c }

func (f *sampleOnceTicker) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

func (f *sampleOnceTicker) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// collectRead is one observed collector read.
type collectRead struct {
	snap ResourceSnapshot
	err  error
	// afterTick is whether the test had released the tick before the read.
	afterTick bool
}

// sampleOnceRecorder wraps a collector, records every read and signals
// the first one, so a test can release the tick only after it.
type sampleOnceRecorder struct {
	inner     func() (ResourceSnapshot, error)
	mu        sync.Mutex
	reads     []collectRead
	ticked    bool
	firstRead chan struct{}
}

func newSampleOnceRecorder(inner func() (ResourceSnapshot, error)) *sampleOnceRecorder {
	return &sampleOnceRecorder{inner: inner, firstRead: make(chan struct{})}
}

func (r *sampleOnceRecorder) collect() (ResourceSnapshot, error) {
	snap, err := r.inner()
	r.mu.Lock()
	r.reads = append(r.reads, collectRead{snap: snap, err: err, afterTick: r.ticked})
	n := len(r.reads)
	r.mu.Unlock()
	if n == 1 {
		close(r.firstRead)
	}
	return snap, err
}

func (r *sampleOnceRecorder) markTicked() {
	r.mu.Lock()
	r.ticked = true
	r.mu.Unlock()
}

func (r *sampleOnceRecorder) observed() []collectRead {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]collectRead(nil), r.reads...)
}

// useSampleOnceCollector points SampleOnce at rec for the test's duration.
func useSampleOnceCollector(t *testing.T, rec *sampleOnceRecorder) {
	t.Helper()
	prev := sampleOnceCollect
	sampleOnceCollect = rec.collect
	t.Cleanup(func() { sampleOnceCollect = prev })
}

// sampleOnceResult is SampleOnce's return pair, carried over a channel.
type sampleOnceResult struct {
	snap ResourceSnapshot
	err  error
}

// runSampleOnce starts SampleOnce, waits for the first collector read,
// calls beforeTick (which may wait for the host to move on), releases one
// tick and returns SampleOnce's result.
func runSampleOnce(t *testing.T, rec *sampleOnceRecorder, clk runtime.Clock, beforeTick func()) (*sampleOnceTicker, sampleOnceResult) {
	t.Helper()
	ticker := newSampleOnceTicker()
	done := make(chan sampleOnceResult, 1)
	go func() {
		snap, err := SampleOnce(clk, ticker)
		done <- sampleOnceResult{snap: snap, err: err}
	}()
	<-rec.firstRead
	if beforeTick != nil {
		beforeTick()
	}
	rec.markTicked()
	select {
	case ticker.c <- struct{}{}:
		return ticker, <-done
	case res := <-done:
		t.Fatalf("SampleOnce returned (%+v, %v) without waiting for its tick", res.snap, res.err)
		return ticker, res
	}
}

// waitForCollectorProgress returns a beforeTick hook. On linux it waits
// until /proc/stat's aggregate jiffy total has moved past its value just
// after the first read, so the second read has a CPU delta to measure (two
// reads inside one jiffy are a counter regression, ErrCPUBaselinePending).
// It reads /proc/stat itself, never through the collector, so the
// collector's own baseline is untouched. Elsewhere it is nil: the darwin
// collector keeps no baseline.
func waitForCollectorProgress(t *testing.T) func() {
	t.Helper()
	if goruntime.GOOS != "linux" {
		return nil
	}
	return func() {
		base := procStatTotal(t)
		deadline := time.After(30 * time.Second)
		for procStatTotal(t) <= base {
			select {
			case <-deadline:
				t.Fatalf("/proc/stat jiffy total stayed at %d for 30s", base)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

// procStatTotal reads /proc/stat's aggregate jiffy total directly.
func procStatTotal(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		t.Fatalf("read /proc/stat: %v", err)
	}
	_, total, err := parseProcStat(data)
	if err != nil {
		t.Fatalf("parse /proc/stat: %v", err)
	}
	return total
}

// TestSampleOnceMemoryAndSwap: over the real platform collector, SampleOnce
// returns exactly the second read's memory and swap figures, stamped with
// the injected clock, after one tick; the first read precedes the tick.
func TestSampleOnceMemoryAndSwap(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows has no platform collector (ErrUnsupportedPlatform); TestSampleOnceReturnsSecondReadError covers the error path")
	}
	rec := newSampleOnceRecorder(collectMetrics)
	useSampleOnceCollector(t, rec)
	clk := runtime.NewFixedClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	ticker, res := runSampleOnce(t, rec, clk, waitForCollectorProgress(t))
	if res.err != nil {
		t.Fatalf("SampleOnce: %v", res.err)
	}
	reads := rec.observed()
	if len(reads) != 2 {
		t.Fatalf("collector reads = %d, want 2", len(reads))
	}
	if reads[0].afterTick || !reads[1].afterTick {
		t.Fatalf("reads not one tick apart: first afterTick=%v second afterTick=%v", reads[0].afterTick, reads[1].afterTick)
	}
	second := reads[1]
	if second.err != nil {
		t.Fatalf("second platform read failed: %v", second.err)
	}
	got := res.snap
	if got.MemTotalBytes == 0 || got.MemTotalBytes != second.snap.MemTotalBytes || got.MemUsedBytes != second.snap.MemUsedBytes {
		t.Errorf("memory = %d/%d, want the second read's %d/%d (non-zero total)",
			got.MemUsedBytes, got.MemTotalBytes, second.snap.MemUsedBytes, second.snap.MemTotalBytes)
	}
	if got.SwapTotalBytes != second.snap.SwapTotalBytes || got.SwapUsedBytes != second.snap.SwapUsedBytes {
		t.Errorf("swap = %d/%d, want the second read's %d/%d",
			got.SwapUsedBytes, got.SwapTotalBytes, second.snap.SwapUsedBytes, second.snap.SwapTotalBytes)
	}
	if !got.SampledAt.Equal(clk.Now()) {
		t.Errorf("SampledAt = %v, want the injected clock's %v", got.SampledAt, clk.Now())
	}
	if got.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, want the live count", got.Goroutines)
	}
	if !ticker.isStopped() {
		t.Error("SampleOnce returned without stopping its ticker")
	}
}

// TestSampleOnceReturnsSecondReadError: a failing second read is returned
// as that error with a zero snapshot, never as a snapshot a caller could
// classify; a failing first read alone is not an error.
func TestSampleOnceReturnsSecondReadError(t *testing.T) {
	sentinel := cascade.New(cascade.KindUnavailable, "governor: synthetic second-read failure")
	firstErr := cascade.New(cascade.KindUnavailable, "governor: synthetic priming failure")
	healthy := ResourceSnapshot{MemTotalBytes: 8 << 30, MemUsedBytes: 2 << 30, SwapTotalBytes: 1 << 30}
	cases := []struct {
		name    string
		reads   []collectRead
		wantErr error
	}{
		{"second read fails", []collectRead{{snap: healthy}, {err: sentinel}}, sentinel},
		{"first read fails only", []collectRead{{err: firstErr}, {snap: healthy}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			rec := newSampleOnceRecorder(func() (ResourceSnapshot, error) {
				r := tc.reads[i]
				i++
				return r.snap, r.err
			})
			useSampleOnceCollector(t, rec)
			clk := runtime.NewFixedClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
			ticker, res := runSampleOnce(t, rec, clk, nil)
			if !ticker.isStopped() {
				t.Error("ticker not stopped")
			}
			if tc.wantErr != nil {
				if res.err != tc.wantErr { //nolint:errorlint // identity: errors.Is compares Kind only
					t.Fatalf("err = %v, want the second read's own error %v", res.err, tc.wantErr)
				}
				if res.snap != (ResourceSnapshot{}) {
					t.Fatalf("snapshot = %+v alongside an error, want zero", res.snap)
				}
				return
			}
			if res.err != nil {
				t.Fatalf("err = %v, want the second read's snapshot", res.err)
			}
			if res.snap.MemTotalBytes != healthy.MemTotalBytes || !res.snap.SampledAt.Equal(clk.Now()) {
				t.Fatalf("snapshot = %+v, want the second read stamped %v", res.snap, clk.Now())
			}
		})
	}
}
