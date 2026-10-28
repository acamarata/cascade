//go:build linux

package governor

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// chainHolds reports whether target is, by identity, one of the errors in
// err's Unwrap chain. errors.Is is not used: taxonomy errors compare equal
// on Kind alone, so any KindUnavailable error would match.
func chainHolds(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target { //nolint:errorlint // identity walk; see doc comment
			return true
		}
	}
	return false
}

// TestSampleOnceLinuxFirstRead: in a fresh process (a fresh collector with
// no CPU baseline) the first read is ErrCPUBaselinePending, and SampleOnce
// still returns the second read's snapshot, which classifies as a posture
// with a memory signal rather than an error.
func TestSampleOnceLinuxFirstRead(t *testing.T) {
	prev := hostProcCollector
	hostProcCollector = &procCollector{read: os.ReadFile}
	t.Cleanup(func() { hostProcCollector = prev })
	rec := newSampleOnceRecorder(collectMetrics)
	useSampleOnceCollector(t, rec)
	clk := runtime.NewFixedClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))

	_, res := runSampleOnce(t, rec, clk, waitForCollectorProgress(t))

	reads := rec.observed()
	if len(reads) != 2 {
		t.Fatalf("collector reads = %d, want 2", len(reads))
	}
	if !chainHolds(reads[0].err, ErrCPUBaselinePending) {
		t.Fatalf("first read err = %v, want ErrCPUBaselinePending", reads[0].err)
	}
	if res.err != nil {
		t.Fatalf("SampleOnce: %v, want the second read's snapshot", res.err)
	}
	if res.snap.MemTotalBytes == 0 || res.snap.MemTotalBytes != reads[1].snap.MemTotalBytes {
		t.Fatalf("MemTotalBytes = %d, want the second read's non-zero %d", res.snap.MemTotalBytes, reads[1].snap.MemTotalBytes)
	}
	switch posture := ClassifySignal(res.snap, clk.Now(), PeriodForHz(0)); posture {
	case PostureSwapAndMemory, PostureMemoryOnly:
	case PostureNoSignal, PostureStale:
		t.Fatalf("posture = %q, want swap+memory or memory-only", posture)
	}
}
