// Package governor holds the fleet resource governor's shared
// low-frequency host-resource sampler (P1-E13-W3-S26-T1). It has no
// admission or throttle logic of its own; S-26.T2's admission controller
// and S-26.T3's throttle ladder both read the same Sampler snapshot so no
// two subsystems issue independent syscalls against the host.
//
// Purpose: define ResourceSnapshot, SamplerConfig, and Sampler — a
//
//	background goroutine that polls platform-specific host metrics at a
//	rate capped at 1Hz when idle and publishes them as a lock-free atomic
//	snapshot.
//
// Inputs: a SamplerConfig ([governor].sampler_hz, 08-INIT-CONFIG-SPEC §3,
//
//	R-14.42), an injected runtime.Clock (SampledAt, R-14.11 — no bare
//	time.Now), an injected runtime.Ticker (pacing, matching the
//	internal/events/scheduler and internal/runtime periodic-emitter
//	pattern — never a real sleep in a test), and an optional *slog.Logger
//	(nil discards, matching internal/daemon.NewManifest's convention).
//
// Outputs: Snapshot() returns the most recent successfully collected
//
//	ResourceSnapshot; a collection failure logs at warn and retains the
//	previous good value rather than publishing a zeroed one.
//
// Constraints: no CGO anywhere in this package; metrics come from
//
//	golang.org/x/sys/unix sysctls (darwin) or /proc file reads (linux).
//	collectMetrics() is declared once per platform file under a matching
//	//go:build tag; the pure text-parsing helpers those platform
//	collectors call (parseProcStat, parseProcMeminfo) live in this
//	UNTAGGED file specifically so they compile and fuzz on every host,
//	including one that lacks their platform's actual collector.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).
package governor

import (
	"context"
	"log/slog"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultSamplerHz is the sampler's rate when SamplerConfig.MaxHz is left
// at its zero value (08-INIT-CONFIG-SPEC §3: "unconfigured -> defaults to
// 1.0").
const DefaultSamplerHz = 1.0

// MaxSamplerHz is the sampler's hard rate ceiling. A configured MaxHz
// above this is clamped down, never rejected — the idle-mode contract is
// "at most 1Hz", not "exactly 1Hz".
const MaxSamplerHz = 1.0

// ErrUnsupportedPlatform is the sentinel collectMetrics returns on a
// platform the sampler does not yet collect real metrics on (windows,
// tier-2 per the contract). Callers must handle it rather than treat it
// as a transient collection failure: retrying will not help.
var ErrUnsupportedPlatform = cascade.New(cascade.KindUnsupported, "governor: resource sampling not supported on this platform")

// ResourceSnapshot is one point-in-time read of host resource state. It is
// value-typed so callers always get an independent copy from Snapshot,
// never a reference into the sampler's internal state.
type ResourceSnapshot struct {
	// CPUFraction is fractional CPU utilization in [0, 1]. Its precision
	// is platform-dependent and documented per collector (see
	// sampler_darwin.go, sampler_linux.go).
	CPUFraction float64 `json:"cpu_fraction"`
	// MemUsedBytes is used physical memory, in bytes.
	MemUsedBytes uint64 `json:"mem_used_bytes"`
	// MemTotalBytes is total physical memory, in bytes.
	MemTotalBytes uint64 `json:"mem_total_bytes"`
	// SwapUsedBytes is used swap, in bytes.
	SwapUsedBytes uint64 `json:"swap_used_bytes"`
	// SwapTotalBytes is total configured swap, in bytes.
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	// Goroutines is the process's live goroutine count at sample time
	// (runtime.NumGoroutine()).
	Goroutines int `json:"goroutines"`
	// SampledAt is when this snapshot was collected, per the sampler's
	// injected Clock — never the platform collector's own wall-clock
	// read (Art.7.3 determinism).
	SampledAt time.Time `json:"sampled_at"`
}

// SamplerConfig configures a Sampler. It is populated from the daemon's
// [governor] config section (08-INIT-CONFIG-SPEC §3, R-14.42).
type SamplerConfig struct {
	// MaxHz is the sampler's maximum tick rate. Zero defaults to
	// DefaultSamplerHz; a value above MaxSamplerHz is clamped to it.
	MaxHz float64 `json:"sampler_hz"`
}

// PeriodForHz converts a configured rate into a tick interval, applying
// SamplerConfig's default-and-clamp rule. Production composition roots use
// it to size the runtime.Ticker they hand to NewSampler; it is exported so
// that wiring never has to re-derive the same arithmetic.
func PeriodForHz(hz float64) time.Duration {
	switch {
	case hz <= 0:
		hz = DefaultSamplerHz
	case hz > MaxSamplerHz:
		hz = MaxSamplerHz
	}
	return time.Duration(float64(time.Second) / hz)
}

// Sampler runs a single background goroutine that polls collectMetrics on
// each tick from the injected runtime.Ticker and publishes the result as a
// lock-free atomic value. The zero value is not usable; construct with
// NewSampler.
type Sampler struct {
	clk     runtime.Clock
	ticker  runtime.Ticker
	log     *slog.Logger
	collect func() (ResourceSnapshot, error)

	snap atomic.Value // holds ResourceSnapshot

	mu     sync.Mutex
	cancel context.CancelFunc

	// afterTick is a test-only hook invoked once per tick, after the
	// snapshot store (or the warn-log, on a collection failure). Always
	// nil in production; it exists solely so a test driving Sampler
	// through Start's real goroutine (rather than calling tick directly)
	// has a deterministic, non-sleep signal that one full tick cycle has
	// completed (R-14.136 — no sleeps as synchronization).
	afterTick func()
}

// NewSampler builds a Sampler. A nil clk falls back to
// runtime.NewSystemClock(); a nil ticker falls back to
// runtime.NewSystemTicker(PeriodForHz(cfg.MaxHz)) — tests should always
// inject a fake Ticker instead of relying on this fallback, matching
// R-14.136. A nil log discards collection-failure warnings, mirroring
// internal/daemon.NewManifest's convention.
func NewSampler(cfg SamplerConfig, clk runtime.Clock, ticker runtime.Ticker, log *slog.Logger) *Sampler {
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	if ticker == nil {
		ticker = runtime.NewSystemTicker(PeriodForHz(cfg.MaxHz))
	}
	return &Sampler{
		clk:     clk,
		ticker:  ticker,
		log:     log,
		collect: collectMetrics,
	}
}

// Start launches the sampler's background goroutine, which ticks until ctx
// is cancelled or Stop is called (whichever comes first), then stops the
// ticker and returns. Start itself does not block.
func (s *Sampler) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	go s.run(runCtx)
}

// Stop cancels the sampler's run loop. Safe to call more than once, and
// safe to call before Start (a no-op in that case): context.CancelFunc is
// itself idempotent, so no extra guard is needed here.
func (s *Sampler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Snapshot returns the most recently collected ResourceSnapshot. It never
// blocks on the sampler's goroutine and is safe to call concurrently from
// any number of readers. Before the first successful tick it returns the
// zero ResourceSnapshot.
func (s *Sampler) Snapshot() ResourceSnapshot {
	v := s.snap.Load()
	if v == nil {
		return ResourceSnapshot{}
	}
	return v.(ResourceSnapshot)
}

// run is the sampler's single background goroutine body.
func (s *Sampler) run(ctx context.Context) {
	defer s.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ticker.C():
			s.tick()
		}
	}
}

// tick collects one sample. On failure it logs at warn and returns without
// touching the published snapshot, so Snapshot() keeps reporting the last
// good value rather than a zeroed one.
func (s *Sampler) tick() {
	snap, err := s.collect()
	if err != nil {
		if s.log != nil {
			s.log.Warn("governor: resource sample collection failed", "error", err)
		}
		if s.afterTick != nil {
			s.afterTick()
		}
		return
	}
	snap.Goroutines = goruntime.NumGoroutine()
	snap.SampledAt = s.clk.Now()
	s.snap.Store(snap)
	if s.afterTick != nil {
		s.afterTick()
	}
}

// parseProcStat extracts the aggregate "cpu " line's idle and total jiffy
// counts from /proc/stat's raw content. It never panics on malformed
// input (FuzzParseProcStat drives it directly): a missing "cpu" line or an
// unparsable field yields a taxonomy error, never an index panic.
func parseProcStat(data []byte) (idle, total uint64, err error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		vals := make([]uint64, 0, len(fields)-1)
		for _, f := range fields[1:] {
			v, perr := strconv.ParseUint(f, 10, 64)
			if perr != nil {
				return 0, 0, cascade.Wrap(cascade.KindInvalidInput, perr, "governor: parse /proc/stat cpu field")
			}
			vals = append(vals, v)
			total += v
		}
		if len(vals) < 4 {
			return 0, 0, cascade.New(cascade.KindInvalidInput, "governor: /proc/stat cpu line has too few fields")
		}
		idle = vals[3]
		return idle, total, nil
	}
	return 0, 0, cascade.New(cascade.KindInvalidInput, "governor: /proc/stat has no cpu line")
}

// parseProcMeminfo extracts MemTotal, MemAvailable, SwapTotal, and
// SwapFree (in bytes, converted from the file's kB units) from
// /proc/meminfo's raw content. Unrecognized or malformed lines are
// skipped rather than treated as errors, so a kernel that adds or reorders
// fields never breaks collection, and FuzzParseProcMeminfo can never drive
// it to a panic — arbitrary bytes simply yield zero values.
func parseProcMeminfo(data []byte) (memTotalBytes, memAvailBytes, swapTotalBytes, swapFreeBytes uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		n, perr := strconv.ParseUint(fields[1], 10, 64)
		if perr != nil {
			continue
		}
		bytesVal := n * 1024
		switch key {
		case "MemTotal":
			memTotalBytes = bytesVal
		case "MemAvailable":
			memAvailBytes = bytesVal
		case "SwapTotal":
			swapTotalBytes = bytesVal
		case "SwapFree":
			swapFreeBytes = bytesVal
		}
	}
	return memTotalBytes, memAvailBytes, swapTotalBytes, swapFreeBytes
}
