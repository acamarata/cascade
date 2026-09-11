package jobs

// Purpose: R-40.X18's live counterpart to R/S-39.T5's synthetic-only
//
//	gate-denied leg (R-21.202 satisfied-by-producer): real
//	CompletionPolicy.Transition denials publish real jobs.gate.denied
//	events on the real event bus, the real supervision.Detector
//	subscribes and normalizes them, and three denials for one JobID
//	inside 30 minutes (a frozen clock) invoke the escalation ladder
//	while two-in-window and three-spread-beyond do not.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// signalRetryer is a real, minimal governor.Retryer (Art.1-exempt in a
// _test.go file, matching stall_test.go's own fakeRetryer precedent):
// every Retry call sends on a buffered channel so the test can observe
// "the ladder was actually invoked" without an unbounded receive
// anywhere -- every read below is select-with-timeout.
type signalRetryer struct{ fired chan string }

func (r signalRetryer) Retry(_ context.Context, entityID string) error {
	select {
	case r.fired <- entityID:
	default:
	}
	return nil
}

type noopEnricher struct{}

func (noopEnricher) Enrich(context.Context, string) (int, error) { return 0, nil }

type gateIDGen struct{ n int }

func (g *gateIDGen) next() string { g.n++; return "attn-" + string(rune('a'+g.n)) }

func newGateDetector(t *testing.T, clock *runtime.FixedClock, fired chan string) (*supervision.Detector, *events.Bus) {
	t.Helper()
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)
	j := journal.New(kv, clock, "gate-stall-test")
	gen := &gateIDGen{}
	store := supervision.NewStore(kv, clock, nil, gen.next, 0)
	policy := governor.EscalationPolicy{
		MaxAttempts: map[governor.EscalationRung]int{
			governor.RungRetry: 1, governor.RungContext: 1,
			governor.RungSupervisorTask: 1, governor.RungHuman: 1,
		},
		ConfidenceThreshold: 0.5,
	}
	det := supervision.NewDetector(j, signalRetryer{fired: fired}, noopEnricher{}, store, nil, policy, time.Minute, clock)
	return det, bus
}

// completionFixtureForGate builds a CompletionPolicy whose every denial
// is a real, independently-driven denial (missing evidence), wired to
// bus so the detector's real subscription sees it.
func completionFixtureForGate(t *testing.T, bus *events.Bus, clock *runtime.FixedClock) (*CompletionPolicy, Job) {
	t.Helper()
	path := t.TempDir() + "/gate.db"
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewStore(db)
	job := baseJob("job-gate")
	job.RiskClass = string(RiskClassNormal)
	job.State = JobStateRunning
	if err := store.PutJob(context.Background(), job); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	authz := NewProducerAuthz(store, func() bool { return true }, nil)
	ledger, err := NewEvidenceLedger(store, clock, &recordingWriter{}, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	cp, err := NewCompletionPolicy(CompletionPolicyDeps{Store: store, Ledger: ledger, Bus: bus, Clock: clock, EngineID: testEngineID})
	if err != nil {
		t.Fatalf("NewCompletionPolicy: %v", err)
	}
	return cp, job
}

// denyOnce drives exactly one real, missing-evidence completion denial
// for job (no evidence was ever seeded), which publishes exactly one
// real jobs.gate.denied event through cp's bus, with SessionID set so
// the detector's normalized StallSignal carries a target to escalate.
func denyOnce(t *testing.T, cp *CompletionPolicy, job Job, sessionID string) {
	t.Helper()
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassNormal, SessionID: sessionID, TicketID: "T-gate",
	})
	if _, ok := err.(*ErrorCompletionDenied); !ok {
		t.Fatalf("denyOnce: Transition = %v, want *ErrorCompletionDenied", err)
	}
}

// TestGateDeniedLiveStallDetector is the exact name the ticket's checks
// list runs (-run '^TestGateDeniedLiveStallDetector$').
func TestGateDeniedLiveStallDetector(t *testing.T) {
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	clock := runtime.NewFixedClock(base)
	fired := make(chan string, 4)
	det, bus := newGateDetector(t, clock, fired)
	cp, job := completionFixtureForGate(t, bus, clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- det.Run(ctx, bus) }()

	// Three real denials for the SAME session/job, all within the
	// 30-minute window (the frozen clock never advances), must fire
	// exactly one escalation.
	denyOnce(t, cp, job, "sess-gate-1")
	denyOnce(t, cp, job, "sess-gate-1")
	denyOnce(t, cp, job, "sess-gate-1")

	select {
	case entity := <-fired:
		if entity != "sess-gate-1" {
			t.Fatalf("escalation fired for %q, want sess-gate-1", entity)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the third gate-denied to escalate")
	}

	cancel()
	select {
	case <-runErrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Detector.Run to return after cancel")
	}
}

func TestGateDeniedLiveStallDetectorTwoInWindowDoesNotFire(t *testing.T) {
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	clock := runtime.NewFixedClock(base)
	fired := make(chan string, 4)
	det, bus := newGateDetector(t, clock, fired)
	cp, job := completionFixtureForGate(t, bus, clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = det.Run(ctx, bus) }()

	denyOnce(t, cp, job, "sess-gate-2")
	denyOnce(t, cp, job, "sess-gate-2")

	select {
	case entity := <-fired:
		t.Fatalf("escalation fired for %q after only two denials, want none", entity)
	case <-time.After(500 * time.Millisecond):
	}
}

// A "three spread beyond the window" live variant is deliberately NOT
// added here: runtime.FixedClock.Advance is not safe for concurrent use
// against a live Detector.Run goroutine reading Now() (confirmed with
// -race during this ticket's own verification), and R/S-39.T5's own
// synthetic suite already covers the beyond-window non-firing case
// directly against StallSignal without a live clock race. See the
// journal.
