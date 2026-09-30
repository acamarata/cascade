// Package governor holds the fleet resource governor's shared
// low-frequency host-resource sampler. The admission controller and the
// throttle ladder read the same Sampler snapshot, so no two subsystems
// issue independent syscalls against the host.
//
// Purpose: ResourceSnapshot, SamplerConfig, and Sampler, a goroutine that
// polls host metrics at most 1Hz and publishes a lock-free snapshot.
//
// Inputs: a SamplerConfig ([governor].sampler_hz), an injected Clock
// (SampledAt; no bare time.Now), an injected Ticker (never a real sleep in
// a test), and an optional *slog.Logger (nil discards).
//
// Outputs: Snapshot() returns the newest successfully collected
// ResourceSnapshot; a failed collection logs at warn and publishes nothing,
// so the retained snapshot ages into stale (admission_signal.go).
//
// Constraints: no CGO; darwin reads sysctls, linux reads /proc.
// collectMetrics() is declared once per platform file under a matching
// build tag; the pure parsers live in this UNTAGGED file so they compile
// and fuzz on every host.
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

// ErrUnsupportedPlatform is what collectMetrics returns on a platform with
// no real collector (windows, tier-2); retrying will not help.
var ErrUnsupportedPlatform = cascade.New(cascade.KindUnsupported, "governor: resource sampling not supported on this platform")

// ResourceSnapshot is one point-in-time read of host resource state,
// value-typed so every Snapshot caller gets an independent copy.
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
	// injected Clock (Art.7.3); zero means never sampled.
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
// SamplerConfig's default-and-clamp rule; composition roots size the
// runtime.Ticker they hand to NewSampler with it.
func PeriodForHz(hz float64) time.Duration {
	switch {
	case hz <= 0:
		hz = DefaultSamplerHz
	case hz > MaxSamplerHz:
		hz = MaxSamplerHz
	}
	return time.Duration(float64(time.Second) / hz)
}

// Sampler polls collectMetrics on each tick of the injected Ticker and
// publishes the result atomically. Construct with NewSampler.
type Sampler struct {
	clk     runtime.Clock
	ticker  runtime.Ticker
	log     *slog.Logger
	collect func() (ResourceSnapshot, error)
	period  time.Duration

	snap atomic.Value // holds ResourceSnapshot

	mu     sync.Mutex
	cancel context.CancelFunc
	// tickHooks run after EVERY tick, successful or failed, before
	// afterTick; stopHooks run on Stop. NewAdmissionController registers
	// its queue re-evaluation and its queue refusal here, so a waiter
	// learns of signal loss or a stopped sampler without a new Admit call.
	tickHooks, stopHooks []func()

	// afterTick is a test-only hook (nil in production) invoked last in
	// every tick: a non-sleep signal that one full tick cycle completed.
	afterTick func()
}

// NewSampler builds a Sampler. A nil clk falls back to the system clock
// and a nil ticker to runtime.NewSystemTicker(PeriodForHz(cfg.MaxHz));
// tests always inject fakes. A nil log discards warnings.
func NewSampler(cfg SamplerConfig, clk runtime.Clock, ticker runtime.Ticker, log *slog.Logger) *Sampler {
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	period := PeriodForHz(cfg.MaxHz)
	if ticker == nil {
		ticker = runtime.NewSystemTicker(period)
	}
	return &Sampler{
		clk:     clk,
		ticker:  ticker,
		log:     log,
		collect: collectMetrics,
		period:  period,
	}
}

// Period is the sampler's tick interval: 1 / the effective (defaulted and
// clamped) MaxHz. Admission's staleness window is measured in it.
func (s *Sampler) Period() time.Duration {
	if s.period <= 0 {
		return PeriodForHz(0)
	}
	return s.period
}

// addHooks registers onTick to run after every tick and onStop to run on
// Stop (see tickHooks).
func (s *Sampler) addHooks(onTick, onStop func()) {
	s.mu.Lock()
	s.tickHooks = append(s.tickHooks, onTick)
	s.stopHooks = append(s.stopHooks, onStop)
	s.mu.Unlock()
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

// Stop cancels the sampler's run loop, then runs the stop hooks outside
// s.mu: a stopped sampler never reports again, so the admission controller
// refuses its queued waiters instead of stranding them. Safe to call more
// than once, and before Start.
func (s *Sampler) Stop() {
	s.mu.Lock()
	cancel, hooks := s.cancel, append([]func(){}, s.stopHooks...)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, h := range hooks {
		h()
	}
}

// Snapshot returns the most recently collected ResourceSnapshot without
// blocking; safe for concurrent readers. Before the first successful tick
// it returns the zero ResourceSnapshot (zero SampledAt: stale).
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

// tick collects one sample. On failure it logs at warn and publishes
// nothing: the previous snapshot keeps its old SampledAt, so readers see it
// age into staleness rather than a zeroed or refreshed value. Either way
// the tick hooks run afterwards.
func (s *Sampler) tick() {
	defer s.runTickHooks()
	snap, err := s.collect()
	if err != nil {
		if s.log != nil {
			s.log.Warn("governor: resource sample collection failed", "error", err)
		}
		return
	}
	snap.Goroutines = goruntime.NumGoroutine()
	snap.SampledAt = s.clk.Now()
	s.snap.Store(snap)
}

// runTickHooks runs the registered tick hooks, then the test-only
// afterTick, outside s.mu.
func (s *Sampler) runTickHooks() {
	s.mu.Lock()
	hooks := append([]func(){}, s.tickHooks...)
	s.mu.Unlock()
	for _, h := range hooks {
		h()
	}
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
// skipped, so arbitrary bytes yield zero values and never a panic
// (FuzzParseProcMeminfo); the linux collector validates MemTotal and
// MemAvailable separately (meminfoMemory).
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
