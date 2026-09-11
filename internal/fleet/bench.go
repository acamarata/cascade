// Package fleet (bench.go) implements the lane bench/probe protocol
// (P1-E12-W3-S25-T5): Probe sends one minimal model.execute round-trip
// through the pkg/provider.ModelExecutor seam and records its outcome;
// Bench fans out N probes at a configured concurrency and aggregates
// p50/p95 latency, error rate, and a rough cost estimate.
//
// SEAM: the ONLY model door this package reaches through is
// pkg/provider.ModelExecutor (R-21.264) - Execute(ctx, ModelRequest)
// (ModelResponse, error), declared in pkg/provider/model.go and satisfied
// by internal/conductor's Conductor. internal/fleet never imports
// internal/conductor.
//
// BOUNDED MEASUREMENT: a probe's own cost must never become the problem
// it measures. Every probe carries TaskClass "code" and Sensitivity
// SensitivityInternal (06-FORGE-SPEC.md §5.16 - bench traffic is
// operational metadata, not user content) and a single-message,
// single-turn request. Bench's Concurrency is clamped to
// MaxBenchConcurrency and N to MaxBenchProbes, so a caller cannot turn one
// Bench call into an unbounded storm against a lane. A probe that cannot
// be obtained (executor error, context deadline, cancellation) is
// recorded as an ERROR outcome, never silently dropped and never
// misreported as a fast success - Bench's ErrorRate would otherwise
// undercount exactly the failures a lane-health consumer most needs to
// see.
//
// SPORT: internal/fleet.bench (ADD, P1-E12-W3-S25-T5).
package fleet

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

// MaxBenchProbes bounds BenchConfig.N so one Bench call can never storm a
// lane with an unbounded probe count.
const MaxBenchProbes = 100

// MaxBenchConcurrency bounds BenchConfig.Concurrency for the same reason.
const MaxBenchConcurrency = 10

// probeTaskClass and probeSensitivity are fixed for every probe: bench
// traffic is operational metadata (06-FORGE-SPEC.md §5.16), never routed,
// priced, or logged as user content.
const probeTaskClass = "code"

// ProbeResult is one minimal model.execute round-trip's outcome.
type ProbeResult struct {
	// LaneID is the lane the probe was addressed to.
	LaneID string
	// LatencyMS is the round-trip latency in milliseconds. Zero only when
	// Err is non-empty (the round trip never completed).
	LatencyMS float64
	// TokensOut is the completion's output token count, from
	// ModelResponse.Usage.OutputTokens.
	TokensOut int
	// ErrorMsg is the executor error's message, or "" on success. A
	// non-empty ErrorMsg means LatencyMS and TokensOut carry no
	// meaningful reading - the probe never obtained one.
	ErrorMsg string
}

// Success reports whether this probe completed without error.
func (r ProbeResult) Success() bool { return r.ErrorMsg == "" }

// BenchConfig configures one Bench run.
type BenchConfig struct {
	// N is the probe count. Non-positive defaults to 1; values above
	// MaxBenchProbes are clamped down, never rejected.
	N int
	// Concurrency is the number of probes in flight at once.
	// Non-positive defaults to 1; values above MaxBenchConcurrency are
	// clamped down.
	Concurrency int
	// Sensitivity is the request sensitivity tier every probe carries. The
	// zero value (and any value Valid() rejects) resolves to
	// provider.SensitivityRestricted - a zero-value BenchConfig can never
	// carry an empty tier into a dispatch (R-21.264).
	Sensitivity provider.SensitivityTier
}

// normalize returns cfg with every field clamped to its safe range.
func (cfg BenchConfig) normalize() BenchConfig {
	out := cfg
	if out.N <= 0 {
		out.N = 1
	}
	if out.N > MaxBenchProbes {
		out.N = MaxBenchProbes
	}
	if out.Concurrency <= 0 {
		out.Concurrency = 1
	}
	if out.Concurrency > MaxBenchConcurrency {
		out.Concurrency = MaxBenchConcurrency
	}
	if !out.Sensitivity.Valid() {
		out.Sensitivity = provider.SensitivityRestricted
	}
	return out
}

// BenchResult aggregates N ProbeResults.
type BenchResult struct {
	// P50MS and P95MS are latency percentiles across every SUCCESSFUL
	// probe. Both are zero when no probe succeeded.
	P50MS float64
	P95MS float64
	// ErrorRate is failed probes / total probes, in [0, 1].
	ErrorRate float64
	// CostEstimate is a rough per-run cost estimate: total output tokens
	// across successful probes. It is a relative, comparable figure
	// across runs, not a currency amount - no pricing table is wired at
	// this ticket.
	CostEstimate float64
}

// Probe sends one minimal model.execute round-trip to laneID through exec
// and records the outcome. ctx governs cancellation/deadline for the
// whole round trip. Probe never returns a non-nil error itself for an
// executor failure - that failure is recorded in the returned
// ProbeResult.ErrorMsg instead, so a caller aggregating many probes (see
// Bench) never has to special-case an error return versus an error
// result. Probe DOES return a non-nil error for a caller-programming
// fault (a nil exec).
func Probe(ctx context.Context, laneID string, exec provider.ModelExecutor) (ProbeResult, error) {
	if exec == nil {
		return ProbeResult{}, errors.New("fleet: Probe requires a non-nil ModelExecutor")
	}
	req := provider.ModelRequest{
		TaskID:      "bench-probe-" + laneID,
		TaskClass:   probeTaskClass,
		Inputs:      []provider.ChatMessage{{Role: "user", Content: "ping"}},
		Sensitivity: provider.SensitivityInternal,
	}
	start, ok := ctx.Value(benchClockKey{}).(func() float64)
	var begin float64
	if ok {
		begin = start()
	}
	resp, err := exec.Execute(ctx, req)
	if err != nil {
		return ProbeResult{LaneID: laneID, ErrorMsg: err.Error()}, nil
	}
	elapsed := 0.0
	if ok {
		elapsed = start() - begin
	}
	return ProbeResult{
		LaneID:    laneID,
		LatencyMS: elapsed,
		TokensOut: resp.Usage.OutputTokens,
	}, nil
}

// benchClockKey is the context key an injected elapsed-time source is
// carried under (Art.7.3 - Probe/Bench never read a bare wall clock).
// WithBenchClock installs one; production wires runtime.Clock through it.
type benchClockKey struct{}

// WithBenchClock returns a context carrying nowMS, a monotonically
// non-decreasing millisecond reading Probe uses to compute LatencyMS.
// Tests inject a deterministic sequence; production wires an
// injected runtime.Clock-backed reading. A context with no installed
// clock makes every ProbeResult.LatencyMS read 0, which is a documented,
// harmless degrade (LatencyMS is diagnostic, never a gating value) rather
// than a panic.
func WithBenchClock(ctx context.Context, nowMS func() float64) context.Context {
	return context.WithValue(ctx, benchClockKey{}, nowMS)
}

// Bench fans out cfg.N probes at cfg.Concurrency through exec and
// aggregates the results. cfg is normalized first (see BenchConfig.
// normalize), so a zero-value cfg is not a hang or a storm - it degrades
// to one probe. A canceled ctx stops issuing new probes but still
// aggregates whatever probes already completed, recording the rest as
// canceled-error outcomes so ErrorRate reflects the true failure count
// rather than silently shrinking N.
//
// Bench itself carries no lane identity (matching the contract's exact
// signature: cfg has no LaneID field) - exec is expected to already be
// scoped to the lane under test (the router resolves placement from the
// ModelRequest, never from a parameter here). A caller attributing a
// BenchResult to a specific lane record (the RPC handler; see rpc.go)
// does so at its own call site, once per lane.
func Bench(ctx context.Context, cfg BenchConfig, exec provider.ModelExecutor) (BenchResult, error) {
	if exec == nil {
		return BenchResult{}, errors.New("fleet: Bench requires a non-nil ModelExecutor")
	}
	cfg = cfg.normalize()

	results := make([]ProbeResult, cfg.N)
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	for i := 0; i < cfg.N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				results[idx] = ProbeResult{ErrorMsg: ctx.Err().Error()}
				return
			}
			r, err := Probe(ctx, "", exec)
			if err != nil {
				r = ProbeResult{ErrorMsg: err.Error()}
			}
			results[idx] = r
		}(i)
	}
	wg.Wait()

	return aggregate(results), nil
}

// aggregate computes BenchResult from a completed probe set.
func aggregate(results []ProbeResult) BenchResult {
	var latencies []float64
	var failed int
	var totalTokens float64
	for _, r := range results {
		if !r.Success() {
			failed++
			continue
		}
		latencies = append(latencies, r.LatencyMS)
		totalTokens += float64(r.TokensOut)
	}
	sort.Float64s(latencies)
	out := BenchResult{
		ErrorRate:    float64(failed) / float64(len(results)),
		CostEstimate: totalTokens,
	}
	out.P50MS = percentile(latencies, 0.50)
	out.P95MS = percentile(latencies, 0.95)
	return out
}

// percentile returns the p-th percentile (0-1) of sorted (ascending), or
// 0 for an empty slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}
