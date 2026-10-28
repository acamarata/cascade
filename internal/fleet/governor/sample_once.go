// Package governor (sample_once.go) takes one host resource sample
// outside a running Sampler, for a short-lived process such as
// `cascade doctor` that needs the posture admission would see without
// starting the sampler goroutine or contacting the daemon.
//
// Purpose: SampleOnce, two reads of the platform collector one tick
//
//	apart. The first read only primes the collector: on linux it records
//	the /proc/stat CPU baseline and returns ErrCPUBaselinePending, so a
//	single read in a fresh process would never publish. The second read
//	is the sample.
//
// Inputs: an injected Clock (SampledAt; no bare time.Now) and an injected
//
//	Ticker (production: runtime.NewSystemTicker(PeriodForHz(0)), the
//	sampler's own period; tests a fake that never sleeps).
//
// Outputs: the second read's ResourceSnapshot with Goroutines and
//
//	SampledAt filled in the way Sampler.tick fills them, or the second
//	read's error. A failed second read is returned as an error, never as
//	a zero snapshot a caller could classify.
//
// Constraints: no second classifier; callers classify the result with
//
//	ClassifySignal, the same rule Admit uses. SampleOnce stops the ticker
//	it is given before returning.
//
// SPORT: internal/fleet/governor.SampleOnce (ADD).
package governor

import (
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/runtime"
)

// sampleOnceCollect is the collector SampleOnce reads. It is the platform
// collector in production; a test wraps it to observe each read without
// replacing what the read returns.
var sampleOnceCollect = collectMetrics

// SampleOnce reads the platform collector twice, one tick of ticker
// apart, and returns the second read stamped with clk.Now(). The first
// read's result is discarded whatever it is: it exists to give the
// collector a baseline. A nil clk falls back to the system clock and a
// nil ticker to one sampler period, as NewSampler does. The ticker is
// stopped before SampleOnce returns.
func SampleOnce(clk runtime.Clock, ticker runtime.Ticker) (ResourceSnapshot, error) {
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	if ticker == nil {
		ticker = runtime.NewSystemTicker(PeriodForHz(0))
	}
	defer ticker.Stop()
	collect := sampleOnceCollect
	// The priming read. Its error is deliberately dropped: on linux the
	// first read in a process always fails with ErrCPUBaselinePending, and
	// any other failure recurs on the second read, which is reported.
	_, _ = collect()
	<-ticker.C()
	snap, err := collect()
	if err != nil {
		return ResourceSnapshot{}, err
	}
	snap.Goroutines = goruntime.NumGoroutine()
	snap.SampledAt = clk.Now()
	return snap, nil
}
