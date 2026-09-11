package fleet

// Purpose: Probe/Bench's unit suite against a deterministic fake
//   ModelExecutor - success, executor-error propagation, context
//   cancellation, and a simulated timeout (Art.7.3 - no sleep-based
//   synchronization). TestProbeRealLane, the tagged live-lane acceptance
//   integration test, lives in bench_integration_test.go behind the
//   `integration` build tag.
// SPORT: internal/fleet.bench (ADD, per T-5 sport_updates).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// fakeExecutor is a deterministic pkg/provider.ModelExecutor: it never
// touches the network (Art.7.2 - the default unit lane forbids importing
// net), and its behavior is fully controlled by the test. sawMu guards
// sawSens/sawTC so concurrent Bench fan-out (multiple goroutines calling
// Execute on the same fake) stays -race clean.
type fakeExecutor struct {
	resp  provider.ModelResponse
	err   error
	calls int32
	block chan struct{} // if non-nil, Execute blocks on this until ctx.Done

	sawMu   sync.Mutex
	sawSens provider.SensitivityTier
	sawTC   string
}

func (f *fakeExecutor) lastSeen() (provider.SensitivityTier, string) {
	f.sawMu.Lock()
	defer f.sawMu.Unlock()
	return f.sawSens, f.sawTC
}

func (f *fakeExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	atomic.AddInt32(&f.calls, 1)
	f.sawMu.Lock()
	f.sawSens = req.Sensitivity
	f.sawTC = req.TaskClass
	f.sawMu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return provider.ModelResponse{}, ctx.Err()
		}
	}
	if f.err != nil {
		return provider.ModelResponse{}, f.err
	}
	return f.resp, nil
}

func fakeClock(startMS, stepMS float64) func() float64 {
	cur := startMS
	first := true
	return func() float64 {
		if first {
			first = false
			return cur
		}
		cur += stepMS
		return cur
	}
}

func TestProbeSuccess(t *testing.T) {
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 42}}}
	ctx := WithBenchClock(context.Background(), fakeClock(1000, 25))

	r, err := Probe(ctx, "lane-a", exec)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if !r.Success() {
		t.Fatalf("r.Success() = false, want true; ErrorMsg=%q", r.ErrorMsg)
	}
	if r.LaneID != "lane-a" {
		t.Fatalf("LaneID = %q, want lane-a", r.LaneID)
	}
	if r.TokensOut != 42 {
		t.Fatalf("TokensOut = %d, want 42", r.TokensOut)
	}
	if r.LatencyMS != 25 {
		t.Fatalf("LatencyMS = %v, want 25", r.LatencyMS)
	}
	sens, tc := exec.lastSeen()
	if sens != provider.SensitivityInternal {
		t.Fatalf("Sensitivity sent = %v, want SensitivityInternal (bench traffic is operational metadata)", sens)
	}
	if tc != "code" {
		t.Fatalf("TaskClass sent = %q, want code", tc)
	}
}

func TestProbeExecutorErrorPropagatesAsResult(t *testing.T) {
	exec := &fakeExecutor{err: errors.New("lane unavailable")}
	r, err := Probe(context.Background(), "lane-b", exec)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil (executor errors are recorded in the result)", err)
	}
	if r.Success() {
		t.Fatal("r.Success() = true, want false")
	}
	if r.ErrorMsg != "lane unavailable" {
		t.Fatalf("ErrorMsg = %q, want %q", r.ErrorMsg, "lane unavailable")
	}
}

func TestProbeNilExecutorRefused(t *testing.T) {
	_, err := Probe(context.Background(), "lane-c", nil)
	if err == nil {
		t.Fatal("Probe() error = nil, want a refusal for a nil ModelExecutor")
	}
}

func TestProbeContextCanceled(t *testing.T) {
	exec := &fakeExecutor{block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before Execute is even called

	r, err := Probe(ctx, "lane-d", exec)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if r.Success() {
		t.Fatal("r.Success() = true, want false for a canceled context")
	}
}

func TestProbeContextDeadlineExceeded(t *testing.T) {
	exec := &fakeExecutor{block: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	r, err := Probe(ctx, "lane-e", exec)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if r.Success() {
		t.Fatal("r.Success() = true, want false for a timed-out context")
	}
	if !errors.Is(context.DeadlineExceeded, context.DeadlineExceeded) {
		t.Fatal("sanity: context.DeadlineExceeded must equal itself")
	}
}

func TestBenchConfigNormalizeClampsAndDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   BenchConfig
		want BenchConfig
	}{
		{"zero value", BenchConfig{}, BenchConfig{N: 1, Concurrency: 1, Sensitivity: provider.SensitivityRestricted}},
		{"over max", BenchConfig{N: 1000, Concurrency: 1000}, BenchConfig{N: MaxBenchProbes, Concurrency: MaxBenchConcurrency, Sensitivity: provider.SensitivityRestricted}},
		{"negative", BenchConfig{N: -5, Concurrency: -5}, BenchConfig{N: 1, Concurrency: 1, Sensitivity: provider.SensitivityRestricted}},
		{"unrecognised sensitivity resolves restricted", BenchConfig{N: 1, Concurrency: 1, Sensitivity: provider.SensitivityTier(200)}, BenchConfig{N: 1, Concurrency: 1, Sensitivity: provider.SensitivityRestricted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.normalize()
			if got != tc.want {
				t.Fatalf("normalize(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBenchAggregatesSuccessAndFailure(t *testing.T) {
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 10}}}
	res, err := Bench(context.Background(), BenchConfig{N: 6, Concurrency: 3}, exec)
	if err != nil {
		t.Fatalf("Bench() error = %v, want nil", err)
	}
	if res.ErrorRate != 0 {
		t.Fatalf("ErrorRate = %v, want 0", res.ErrorRate)
	}
	if res.CostEstimate != 60 {
		t.Fatalf("CostEstimate = %v, want 60 (6 probes * 10 tokens)", res.CostEstimate)
	}
	if atomic.LoadInt32(&exec.calls) != 6 {
		t.Fatalf("exec.calls = %d, want 6", exec.calls)
	}
}

func TestBenchAllFailuresErrorRateOne(t *testing.T) {
	exec := &fakeExecutor{err: errors.New("boom")}
	res, err := Bench(context.Background(), BenchConfig{N: 4, Concurrency: 2}, exec)
	if err != nil {
		t.Fatalf("Bench() error = %v, want nil", err)
	}
	if res.ErrorRate != 1.0 {
		t.Fatalf("ErrorRate = %v, want 1.0", res.ErrorRate)
	}
	if res.P50MS != 0 || res.P95MS != 0 {
		t.Fatalf("P50MS/P95MS = %v/%v, want 0/0 with zero successful probes", res.P50MS, res.P95MS)
	}
}

func TestBenchNilExecutorRefused(t *testing.T) {
	_, err := Bench(context.Background(), BenchConfig{N: 1}, nil)
	if err == nil {
		t.Fatal("Bench() error = nil, want a refusal for a nil ModelExecutor")
	}
}

func TestBenchCanceledContextRecordsRemainingAsFailures(t *testing.T) {
	exec := &fakeExecutor{resp: provider.ModelResponse{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := Bench(ctx, BenchConfig{N: 5, Concurrency: 2}, exec)
	if err != nil {
		t.Fatalf("Bench() error = %v, want nil", err)
	}
	if res.ErrorRate != 1.0 {
		t.Fatalf("ErrorRate = %v, want 1.0 (every probe skipped on an already-canceled context)", res.ErrorRate)
	}
}

func TestPercentileEmptyIsZero(t *testing.T) {
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("percentile(nil) = %v, want 0", got)
	}
}
