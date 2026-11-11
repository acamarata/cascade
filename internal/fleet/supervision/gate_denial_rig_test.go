package supervision_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
	_ "modernc.org/sqlite"
)

var denialT0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// denialClock protects the shared FixedClock while Run consumes bus events.
type denialClock struct {
	mu sync.Mutex
	*runtime.FixedClock
}

func (c *denialClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.FixedClock.Now()
}

func (c *denialClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.FixedClock.Advance(d)
}

type denialSessions struct{}

// Get supplies the two active sessions used by the subject and barrier.
func (denialSessions) Get(_ context.Context, id string) (sessions.SessionRecord, error) {
	if id != "S1" && id != "S9" {
		return sessions.SessionRecord{}, sessions.ErrNotFound
	}
	return sessions.SessionRecord{SessionID: id, State: "active", UpdatedAt: denialT0.UnixMilli()}, nil
}

// List seeds the detector with the same active records returned by Get.
func (s denialSessions) List(ctx context.Context, _ sessions.Filter) ([]sessions.SessionRecord, error) {
	a, _ := s.Get(ctx, "S1")
	b, _ := s.Get(ctx, "S9")
	return []sessions.SessionRecord{a, b}, nil
}

type denialApprovals struct{}

// RequestApproval supplies the required human rung seam.
func (denialApprovals) RequestApproval(context.Context, string, supervision.StallEvent) (string, error) {
	return "approval", nil
}

type denialRig struct {
	t            *testing.T
	ctx          context.Context
	clock        *denialClock
	db           *sql.DB
	bus          *events.Bus
	store        *jobs.Store
	leases       *jobs.LeaseManager
	reservations *economics.ReservationStore
	policy       *jobs.CompletionPolicy
	attention    *supervision.Store
	dirs         *supervision.DirectiveStore
	jr           *journal.SQLiteStore
	d            *supervision.Detector
	mu           sync.Mutex // Publish precedes RecordGateDenial; LastDenial must wait for both.
	last         map[string]supervision.GateDenial
	calls        map[string][5]int // FailJob, Releases, release callback, RetryHint, LastDenial.
	current      string
	barriers     int
}

func denialCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func denialEventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		goruntime.Gosched()
	}
	t.Fatal("published gate denial did not reach detector barrier within 5s")
}

func newDenialRig(t *testing.T) *denialRig {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, root)
	}
	r := &denialRig{t: t, ctx: context.Background(), clock: &denialClock{FixedClock: runtime.NewFixedClock(denialT0)}, last: make(map[string]supervision.GateDenial), calls: make(map[string][5]int)}
	var err error
	r.db, err = sql.Open("sqlite", filepath.Join(root, "jobs.db"))
	denialCheck(t, err)
	r.db.SetMaxOpenConns(1)
	t.Cleanup(func() { denialCheck(t, r.db.Close()) })
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{jobs.ApplyJobsSchema, jobs.ApplyEvidenceSchema, jobs.ApplyGateAttemptsSchema, economics.ApplyReservationSchema} {
		denialCheck(t, apply(r.ctx, r.db, migrate.SQLiteEmitter{}, r.clock, "", ""))
	}
	kv, err := sqlite.Open(r.ctx, filepath.Join(root, "stores.db"))
	denialCheck(t, err)
	t.Cleanup(func() { denialCheck(t, kv.Close()) })
	r.bus = events.New(kv, r.clock)
	r.store = jobs.NewStore(r.db)
	r.leases = jobs.NewLeaseManager(r.store, r.clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	jobs.WireLeaseRelease(r.store, r.leases)
	r.reservations, err = economics.NewReservationStore(r.db, r.clock)
	denialCheck(t, err)
	writer, err := audit.NewEffectLog(audit.New(kv, r.clock, nil))
	denialCheck(t, err)
	ledger, err := jobs.NewEvidenceLedger(r.store, r.clock, writer, jobs.NewProducerAuthz(r.store, func() bool { return true }, nil))
	denialCheck(t, err)
	r.policy, err = jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{Store: r.store, Ledger: ledger, Bus: r.bus, Clock: r.clock, EngineID: "gate-test"})
	denialCheck(t, err)
	r.compose(kv)
	r.seed("J1")
	return r
}

func (r *denialRig) compose(kv provider.Store) {
	r.dirs = supervision.NewDirectiveStore(kv, r.clock)
	r.jr = journal.New(kv, r.clock, "gate-denial-test")
	var id int
	r.attention = supervision.NewStore(kv, r.clock, nil, func() string { id++; return fmt.Sprintf("attention-%d", id) }, 0)
	policy := governor.EscalationPolicy{MaxAttempts: map[governor.EscalationRung]int{governor.RungRetry: 1, governor.RungContext: 1, governor.RungSupervisorTask: 1, governor.RungHuman: 1}, ConfidenceThreshold: 0.5, RungDelay: time.Minute}
	var err error
	r.d, err = supervision.NewStallDetector(r.jr, supervision.RungConfig{Sessions: denialSessions{}, Directives: r.dirs, HydrationEnabled: true, BudgetTokens: 4096}, r.attention, denialApprovals{}, policy, time.Minute, r.clock, supervision.NewBusStallPublisher(r.bus))
	denialCheck(r.t, err)
	denialCheck(r.t, r.d.WithGateStallActions(r.actions()))
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx, r.bus) }()
	r.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			denialCheck(r.t, err)
		case <-time.After(5 * time.Second):
			r.t.Error("Run did not stop")
		}
	})
	denialEventually(r.t, r.d.Alive)
}

func (r *denialRig) seed(id string) {
	denialCheck(r.t, r.store.PutJob(r.ctx, jobs.Job{ID: id, State: jobs.JobStateReviewing, MutableScope: "src/**", RiskClass: string(jobs.RiskClassNormal), MinTaskClass: "code", ConsequenceClass: jobs.ConsequenceNormal, DataClass: new(provider.SensitivityInternal)}))
	denialCheck(r.t, r.store.PutExecution(r.ctx, jobs.Execution{ID: "exec-" + id, JobID: id, Attempt: 1, State: jobs.ExecutionRunning}))
	_, err := r.reservations.Insert(r.ctx, economics.Reservation{ID: "reservation-" + id, ExecutionID: "exec-" + id, JobID: id, ProjectID: "project", RepoID: "repo", ScopeGlobs: []string{"src/**"}, Kind: economics.ReservationInteractive, State: economics.ReservationHeld, ActualSource: economics.ActualSourceEstimated})
	denialCheck(r.t, err)
}

func (r *denialRig) count(id string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls[id]
	c[n]++
	r.calls[id] = c
}

func (r *denialRig) actions() supervision.GateStallActions {
	return supervision.GateStallActions{
		LastDenial: func(_ context.Context, id string) (supervision.GateDenial, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.current = id
			c := r.calls[id]
			c[4]++
			r.calls[id] = c
			d, ok := r.last[id]
			if !ok {
				return d, fmt.Errorf("missing denial: %s", id)
			}
			return d, nil
		},
		FailJob: func(ctx context.Context, id string, d supervision.GateDenial) error {
			r.count(id, 0)
			if err := r.store.PutExecutionResult(ctx, jobs.ExecutionResult{ExecutionID: "exec-" + id, ErrorKind: cascade.KindPolicyDenied.String(), ErrorMessage: "gate_attempts_exhausted: " + d.Reason}); err != nil {
				return err
			}
			return r.store.PutTransition(ctx, id, jobs.JobStateFailed, r.clock.Now().Unix())
		},
		Releases: func(ctx context.Context, id string) ([]nodes.ScopeRelease, error) {
			r.count(id, 1)
			return []nodes.ScopeRelease{func() error {
				r.count(id, 2)
				_, err := r.reservations.Transition(ctx, "reservation-"+id, economics.ReservationReleased)
				return err
			}}, nil
		},
		RetryHint: func(attempt int) (time.Duration, error) {
			r.mu.Lock()
			id := r.current
			r.mu.Unlock()
			r.count(id, 3)
			return (jobs.GateAttemptPolicy{}).Delay(attempt)
		},
	}
}

func (r *denialRig) deny(id, session string, at time.Duration) supervision.GateDenial {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock.Advance(denialT0.Add(at).Sub(r.clock.Now()))
	job, ok, err := r.store.GetJob(r.ctx, id)
	denialCheck(r.t, err)
	if !ok {
		r.t.Fatalf("missing job %s", id)
	}
	err = r.policy.Transition(r.ctx, jobs.TransitionRequest{Job: &job, Target: jobs.JobStateAccepted, Caller: jobs.PolicyEngineIdentity{EngineID: "gate-test"}, PlannedRiskClass: jobs.RiskClassNormal, SessionID: session})
	var denied *jobs.ErrorCompletionDenied
	if !errors.As(err, &denied) {
		r.t.Fatalf("Transition = %v, want completion denial", err)
	}
	attempt, err := r.store.RecordGateDenial(r.ctx, id, denied.Error())
	denialCheck(r.t, err)
	d := supervision.GateDenial{Attempt: attempt, Reason: denied.Error(), DeniedAt: r.clock.Now()}
	r.last[id] = d
	return d
}

// barrier stalls a fresh control job on the same ordered gate subscription.
func (r *denialRig) barrier() {
	r.barriers++
	id := fmt.Sprintf("barrier-%d", r.barriers)
	r.seed(id)
	at := r.clock.Now().Sub(denialT0)
	for range 3 {
		r.deny(id, "S9", at)
	}
	denialEventually(r.t, func() bool {
		for _, it := range r.items("S9") {
			if it.SourceRef == r.source(id) {
				return true
			}
		}
		return false
	})
}

func (r *denialRig) items(session string) []supervision.AttentionItem {
	items, err := r.attention.ListInScopes(r.ctx, []supervision.ScopeRef{{Kind: "session", ID: session}}, supervision.Filter{IncludeAcked: true})
	denialCheck(r.t, err)
	return items
}

func (r *denialRig) source(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.last[id]
	return fmt.Sprintf("gate-stall:%s attempt %d/3; next retry not before %s; reason: %s", id, d.Attempt, d.DeniedAt.Add(45*time.Minute).UTC().Format(time.RFC3339), d.Reason)
}
