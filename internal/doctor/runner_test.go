package doctor

import (
	"context"
	"testing"
	"time"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// fixedTestClock is the shared deterministic Clock every test file in
// this package uses (Art.7.3 — never the system clock in a test).
func fixedTestClock() cascaderuntime.Clock {
	return cascaderuntime.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
}

func TestRun_HappyPathAllOK(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "a", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
		&fakeCheck{name: "b", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
	}
	report := Run(context.Background(), checks, RunOptions{Clock: fixedTestClock()})
	if len(report.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(report.Entries))
	}
	if report.Outcome() != OutcomeOK {
		t.Fatalf("got outcome=%v, want OutcomeOK", report.Outcome())
	}
	if DefaultOutcomeExitCode(report.Outcome()) != 0 {
		t.Fatalf("got exit code=%d, want 0", DefaultOutcomeExitCode(report.Outcome()))
	}
}

func TestRun_AnyErrorOutcome(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "ok", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
		&fakeCheck{name: "bad", runFn: func(context.Context) (CheckResult, error) {
			return CheckResult{Status: StatusError, Message: "boom"}, nil
		}},
	}
	report := Run(context.Background(), checks, RunOptions{Clock: fixedTestClock()})
	if report.Outcome() != OutcomeError {
		t.Fatalf("got outcome=%v, want OutcomeError", report.Outcome())
	}
	okCode := DefaultOutcomeExitCode(OutcomeOK)
	warnCode := DefaultOutcomeExitCode(OutcomeWarn)
	errCode := DefaultOutcomeExitCode(OutcomeError)
	if okCode == warnCode || warnCode == errCode || okCode == errCode {
		t.Fatalf("outcome exit codes are not pairwise distinct: ok=%d warn=%d error=%d", okCode, warnCode, errCode)
	}
}

func TestRun_AnyWarnNoErrorOutcome(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "ok", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
		&fakeCheck{name: "warn", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusWarn}, nil }},
	}
	report := Run(context.Background(), checks, RunOptions{Clock: fixedTestClock()})
	if report.Outcome() != OutcomeWarn {
		t.Fatalf("got outcome=%v, want OutcomeWarn", report.Outcome())
	}
}

func TestRun_FirstRunFiltering(t *testing.T) {
	reg := NewCheckRegistry()
	reg.Register(&fakeCheck{name: "fr", meta: CheckMeta{FirstRun: true}})
	reg.Register(&fakeCheck{name: "not-fr", meta: CheckMeta{FirstRun: false}})

	report := Run(context.Background(), reg.FirstRun(), RunOptions{FirstRunOnly: true, Clock: fixedTestClock()})
	if len(report.Entries) != 1 || report.Entries[0].Name != "fr" {
		t.Fatalf("got entries=%+v, want only [fr]", report.Entries)
	}

	full := Run(context.Background(), reg.List(), RunOptions{Clock: fixedTestClock()})
	if len(full.Entries) != 2 {
		t.Fatalf("got %d entries for a plain run, want 2 (both checks)", len(full.Entries))
	}
}

func TestDoctorPanicRecovery(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "panics", runFn: func(context.Context) (CheckResult, error) {
			panic("kaboom")
		}},
		&fakeCheck{name: "fine", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
	}
	report := Run(context.Background(), checks, RunOptions{Clock: fixedTestClock()})
	if len(report.Entries) != 2 {
		t.Fatalf("a panicking check must not drop the other check's result: got %d entries", len(report.Entries))
	}
	var panicked, fine *ReportEntry
	for i := range report.Entries {
		switch report.Entries[i].Name {
		case "panics":
			panicked = &report.Entries[i]
		case "fine":
			fine = &report.Entries[i]
		}
	}
	if panicked == nil || panicked.Result.Status != StatusError {
		t.Fatalf("got panicked entry=%+v, want StatusError", panicked)
	}
	if fine == nil || fine.Result.Status != StatusOK {
		t.Fatalf("a panic in one check must not affect the sibling check's result: got %+v", fine)
	}
}

// TestRun_HangingCheckIsIsolated proves a check that never returns does
// not hide the rest of the batch: it is bounded by CheckTimeout and
// reported as an error entry while its sibling still completes.
// R-14.136: no sleeps — the hang is bounded by ctx.Done(), not a
// test-side time.Sleep.
func TestRun_HangingCheckIsIsolated(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "hangs", runFn: func(ctx context.Context) (CheckResult, error) {
			<-ctx.Done()
			return CheckResult{Status: StatusError, Message: "timed out"}, nil
		}},
		&fakeCheck{name: "fast", runFn: func(context.Context) (CheckResult, error) { return CheckResult{Status: StatusOK}, nil }},
	}
	report := Run(context.Background(), checks, RunOptions{CheckTimeout: 20 * time.Millisecond, Clock: fixedTestClock()})
	if len(report.Entries) != 2 {
		t.Fatalf("a hanging check must not hide its sibling: got %d entries", len(report.Entries))
	}
}

func TestDoctorFixIdempotent(t *testing.T) {
	applied := false
	fixable := &fakeCheck{
		name: "fixable",
		meta: CheckMeta{Fixable: true},
		runFn: func(context.Context) (CheckResult, error) {
			if applied {
				return CheckResult{Status: StatusOK}, nil
			}
			return CheckResult{Status: StatusError, Message: "needs fixing"}, nil
		},
		fixFn: func(context.Context) (FixResult, error) {
			if applied {
				return FixResult{Applied: false, Delta: ""}, nil
			}
			applied = true
			return FixResult{Applied: true, Delta: "fixed the thing"}, nil
		},
	}
	checks := []Check{fixable}

	first := Run(context.Background(), checks, RunOptions{Fix: true, Clock: fixedTestClock()})
	if !first.Entries[0].Fixed || !first.Entries[0].FixedBy.Applied || first.Entries[0].FixedBy.Delta == "" {
		t.Fatalf("first --fix run: got %+v, want Applied=true with a non-empty delta", first.Entries[0])
	}

	second := Run(context.Background(), checks, RunOptions{Fix: true, Clock: fixedTestClock()})
	if second.Outcome() != OutcomeOK {
		t.Fatalf("second run on an already-fixed system: got outcome=%v, want OutcomeOK", second.Outcome())
	}
	// The already-correct system's Run reports StatusOK, so applyFixes
	// never calls Fix again for it (Fixed stays false) — this IS the
	// idempotency contract: a second run converges to zero mutation
	// without needing to re-invoke Fix at all.
	if second.Entries[0].Fixed {
		t.Fatalf("an already-OK check must not have Fix invoked again: got %+v", second.Entries[0])
	}
}

func TestRun_NonFixableCheckIsNeverFixCalled(t *testing.T) {
	fixCalled := false
	c := &fakeCheck{
		name: "not-fixable",
		meta: CheckMeta{Fixable: false},
		runFn: func(context.Context) (CheckResult, error) {
			return CheckResult{Status: StatusError, Message: "broken"}, nil
		},
		fixFn: func(context.Context) (FixResult, error) {
			fixCalled = true
			return FixResult{}, ErrCheckNotFixable
		},
	}
	report := Run(context.Background(), []Check{c}, RunOptions{Fix: true, Clock: fixedTestClock()})
	if fixCalled {
		t.Fatalf("Fix must never be called on a Fixable=false check")
	}
	if report.Entries[0].Fixed {
		t.Fatalf("got Fixed=true, want false for a non-fixable check")
	}
}

func TestRun_ErrorReturnFromRunBecomesStatusError(t *testing.T) {
	c := &fakeCheck{name: "erroring", runFn: func(context.Context) (CheckResult, error) {
		return CheckResult{}, context.DeadlineExceeded
	}}
	report := Run(context.Background(), []Check{c}, RunOptions{Clock: fixedTestClock()})
	if report.Entries[0].Result.Status != StatusError {
		t.Fatalf("got status=%v, want StatusError when Run returns a non-nil error", report.Entries[0].Result.Status)
	}
}

func TestResolveConcurrency(t *testing.T) {
	if resolveConcurrency(3) != 3 {
		t.Fatalf("resolveConcurrency(3) should return 3 unchanged")
	}
	if got := resolveConcurrency(0); got <= 0 {
		t.Fatalf("resolveConcurrency(0) should default to a positive value, got %d", got)
	}
}
