package doctor

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// Purpose: the bounded-concurrency Runner that executes every registered
//
//	Check and collects a RunReport (ticket contract task 2).
//
// Inputs: a CheckRegistry (or an explicit check slice) plus RunOptions.
// Outputs: a RunReport — one ReportEntry per check run, in Name-sorted
//
//	order regardless of completion order (deterministic for golden
//	output/tests).
//
// Constraints: a hanging or panicking check must never hide the other
//
//	checks' results (ticket contract's "value proposition" note) — each
//	check gets its own context.WithTimeout derived from ctx, and a
//	recover() wraps the Run call so a panic degrades to a StatusError
//	CheckResult instead of crashing the run. Concurrency is bounded to
//	min(runtime.NumCPU(), 8) by default. No bare time.Now()/time.Since —
//	RunReport.GeneratedAt comes from an injected cascaderuntime.Clock.
//
// SPORT: placeholder: doctor/framework (ADD).

// defaultCheckTimeout is the per-check deadline when RunOptions.CheckTimeout
// is zero.
const defaultCheckTimeout = 30 * time.Second

// defaultMaxConcurrency caps the goroutine pool when RunOptions.Concurrency
// is zero, independent of how many cores the host reports (a doctor run is
// I/O-bound diagnostic work, not compute; an unbounded pool serves no
// purpose and only adds scheduler noise on very large machines).
const defaultMaxConcurrency = 8

// ReportEntry is one check's outcome within a RunReport.
type ReportEntry struct {
	Name    string      `json:"name"`
	Result  CheckResult `json:"result"`
	FixedBy FixResult   `json:"fix,omitempty"`
	// Fixed reports whether Fix was attempted for this entry at all
	// (distinct from FixedBy.Applied, which is only meaningful when
	// Fixed is true) — --fix dispatch is opt-in per run, so a plain
	// `cascade doctor` leaves every entry's Fixed=false.
	Fixed bool `json:"fixed"`
}

// Outcome is a RunReport's overall verdict, resolved through the A-T7
// taxonomy mapping at the CLI boundary (handler.go / a future exit-code
// table) — this package only distinguishes the three outcomes named by
// the ticket contract's AC, it fixes no literal exit code.
type Outcome string

const (
	// OutcomeOK means every entry reported StatusOK.
	OutcomeOK Outcome = "ok"
	// OutcomeWarn means no entry reported StatusError, but at least one
	// reported StatusWarn.
	OutcomeWarn Outcome = "warn"
	// OutcomeError means at least one entry reported StatusError.
	OutcomeError Outcome = "error"
)

// RunReport is the Runner's full result.
type RunReport struct {
	Entries     []ReportEntry `json:"entries"`
	GeneratedAt time.Time     `json:"generated_at"`
}

// Outcome resolves the report's overall verdict: any StatusError entry
// makes the outcome OutcomeError; otherwise any StatusWarn entry makes it
// OutcomeWarn; an empty or all-StatusOK report is OutcomeOK.
func (r RunReport) Outcome() Outcome {
	sawWarn := false
	for _, e := range r.Entries {
		switch e.Result.Status {
		case StatusError:
			return OutcomeError
		case StatusWarn:
			sawWarn = true
		case StatusOK:
		}
	}
	if sawWarn {
		return OutcomeWarn
	}
	return OutcomeOK
}

// RunOptions configures a Runner invocation.
type RunOptions struct {
	// FirstRunOnly runs only Metadata().FirstRun checks (--first-run).
	FirstRunOnly bool
	// Fix, when true, calls Fix on every check whose Run reported
	// warn/error and whose Metadata().Fixable is true, after all Run
	// calls complete (--fix).
	Fix bool
	// Concurrency bounds the goroutine pool; <=0 defaults to
	// min(runtime.NumCPU(), defaultMaxConcurrency).
	Concurrency int
	// CheckTimeout bounds each individual check's context; <=0 defaults
	// to defaultCheckTimeout. Tests set this small to exercise the
	// hang-isolation path without a real 30s wait.
	CheckTimeout time.Duration
	// Clock supplies RunReport.GeneratedAt; nil defaults to
	// cascaderuntime.NewSystemClock() (production only — tests must
	// inject a FixedClock, Art.7.3).
	Clock cascaderuntime.Clock
}

// resolveConcurrency applies RunOptions.Concurrency's default.
func resolveConcurrency(n int) int {
	if n > 0 {
		return n
	}
	if c := runtime.NumCPU(); c < defaultMaxConcurrency {
		return c
	}
	return defaultMaxConcurrency
}

// Run executes checks (typically registry.List() or registry.FirstRun())
// under opts and returns the collected RunReport.
func Run(ctx context.Context, checks []Check, opts RunOptions) RunReport {
	timeout := opts.CheckTimeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	clock := opts.Clock
	if clock == nil {
		clock = cascaderuntime.NewSystemClock()
	}

	entries := runChecksBounded(ctx, checks, resolveConcurrency(opts.Concurrency), timeout)
	if opts.Fix {
		applyFixes(ctx, checks, entries, timeout)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return RunReport{Entries: entries, GeneratedAt: clock.Now()}
}

// runChecksBounded runs every check in its own goroutine, at most
// concurrency at a time, each under its own timeout-bound context
// derived from ctx, with panic recovery.
func runChecksBounded(ctx context.Context, checks []Check, concurrency int, timeout time.Duration) []ReportEntry {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	entries := make([]ReportEntry, 0, len(checks))

	for _, c := range checks {
		c := c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			result := runOneChecked(ctx, c, timeout)
			mu.Lock()
			entries = append(entries, ReportEntry{Name: c.Name(), Result: result})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return entries
}

// runOneChecked runs a single check under a per-check deadline, recovering
// a panic into a StatusError CheckResult so one bad check never aborts the
// batch.
func runOneChecked(ctx context.Context, c Check, timeout time.Duration) (result CheckResult) {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			result = CheckResult{
				Status:  StatusError,
				Message: fmt.Sprintf("check %q panicked", c.Name()),
				Detail:  fmt.Sprintf("%v", r),
			}
		}
	}()

	res, err := c.Run(checkCtx)
	if err != nil {
		return CheckResult{Status: StatusError, Message: err.Error(), Detail: res.Detail}
	}
	return res
}

// applyFixes calls Fix on every entry whose Result.Status is warn/error
// and whose owning check declares Fixable=true, mutating entries in
// place. Non-fixable or already-OK checks are left with Fixed=false. A
// panicking Fix is recovered the same way runOneChecked recovers Run.
func applyFixes(ctx context.Context, checks []Check, entries []ReportEntry, timeout time.Duration) {
	byName := make(map[string]Check, len(checks))
	for _, c := range checks {
		byName[c.Name()] = c
	}
	for i := range entries {
		e := &entries[i]
		if e.Result.Status == StatusOK {
			continue
		}
		c, ok := byName[e.Name]
		if !ok || !c.Metadata().Fixable {
			continue
		}
		e.Fixed = true
		e.FixedBy = runOneFix(ctx, c, timeout)
	}
}

// runOneFix calls Fix under a per-check deadline with panic recovery,
// mirroring runOneChecked. A returned ErrCheckNotFixable (a check
// misdeclaring Fixable=true) is reported as an unapplied fix rather than
// propagated, since RunReport carries no separate error channel per
// entry.
func runOneFix(ctx context.Context, c Check, timeout time.Duration) (result FixResult) {
	fixCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			result = FixResult{Applied: false, Delta: fmt.Sprintf("fix panicked: %v", r)}
		}
	}()

	res, err := c.Fix(fixCtx)
	if err != nil {
		return FixResult{Applied: false, Delta: err.Error()}
	}
	return res
}
