// Purpose: schedule.go's registration + fire tests: the real-stack fire
// (Art.2 -- real capture, real pipeline, real age, real fs target, a real
// non-empty proof), the multi-target due-fire selection (exactly one
// target's Outcome per fire), the scheduled/cron-triggered path's no-
// elevation-bypass refusal (through the real Scheduler.Tick, an allow
// gate, and an empty proof), and skip-missed inheritance from C/S-04.T4.
// SPORT: internal.backup.schedule/ADD (tests) (P1-E19-W4-S42-T1).
package backup

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

const testScheduleNamespace = "backup-schedule-test"

// allowGate is a scheduler.ActionGate fake that allows every dispatch and
// records the actions it routed -- this package's own fake, since
// scheduler's stubGate (scheduler_route_test.go) is unexported to that
// package.
type allowGate struct{ seen []routing.Action }

func (g *allowGate) RouteAction(_ context.Context, action routing.Action) (policy.Verdict, policy.Trace, error) {
	g.seen = append(g.seen, action)
	return policy.VerdictAllow, policy.Trace{}, nil
}

func testGateSubject() policy.Subject {
	return policy.Subject{Kind: policy.SubjectAgent, ID: "backup-scheduler-test"}
}

// newTestScheduler builds a real Scheduler with an allow-everything gate
// installed, over an in-memory Store and a frozen Clock the caller can
// advance.
func newTestScheduler(t *testing.T) (*scheduler.Scheduler, *testkit.FrozenClock, provider.Store) {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(store, clock)
	sched := scheduler.New(store, testScheduleNamespace, clock, bus, "owner-a", 1000*time.Hour)
	if err := sched.SetActionGate(&allowGate{}, testGateSubject(), "backup.scheduler.dispatch"); err != nil {
		t.Fatalf("SetActionGate: %v", err)
	}
	return sched, clock, store
}

// TestBackupJobFire is the Art.2 real-stack proof: a real fs Target, real
// SQLiteCapture over a real SQLite handle, the real pipeline, and real
// age -- driven directly through fireBackupTarget with a real (non-empty)
// ElevationProof, exactly the way an authorized attended run would. This
// is NOT the unattended scheduled path (that is
// TestScheduledCreateElevationRefusal below) -- it proves the mechanism
// CAN complete a real backup when authorized, so "no elevation bypass"
// means the unattended path never supplies that authorization, not that
// the machinery is fake.
func TestBackupJobFire(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	pubKey := setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "ns", "k1", []byte("real domain content for the fire test"))

	target := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	domains := map[string]Exporter{
		"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
	}

	m, err := fireBackupTarget(ctx, store, testScheduleNamespace, "proof-1", target, nil, nil, clock, recipient, domains, nil)
	if err != nil {
		t.Fatalf("fireBackupTarget (authorized): %v", err)
	}
	if m.ObjectCount == 0 {
		t.Fatal("fired manifest has zero objects; the real capture did not run")
	}
	if err := VerifyManifest(m, pubKey, nil); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	outs, err := ListOutcomes(ctx, store, testScheduleNamespace, "nas")
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(outs) != 1 || !outs[0].Success || outs[0].Snapshot != string(m.Snapshot) {
		t.Fatalf("ListOutcomes = %+v, want one successful outcome naming snapshot %q", outs, m.Snapshot)
	}
}

// TestScheduledCreateElevationRefusal proves the unattended, cron-triggered
// fire path: registered via RegisterBackupJob, allowed by the ActionGate,
// but the fire itself always uses an empty ElevationProof so
// CreateSnapshot refuses (ErrElevationRequired) and takes NO snapshot --
// recorded as the job's failure Outcome, never a silent skip.
func TestScheduledCreateElevationRefusal(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)
	target := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	pol := TargetPolicy{Target: "nas", CronSpec: "@every 1h", Domains: []string{"context"}}

	if err := RegisterBackupJob(ctx, sched, store, testScheduleNamespace, target, pol, clock, nil, nil); err != nil {
		t.Fatalf("RegisterBackupJob: %v", err)
	}
	if _, err := sched.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close(context.Background()) })

	clock.Advance(2 * time.Hour)
	report, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(report.Fired) != 1 {
		t.Fatalf("Tick report.Fired = %+v, want exactly one attempted fire", report.Fired)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Tick report.Errors = %v, want exactly one refusal (ErrElevationRequired)", report.Errors)
	}

	outs, err := ListOutcomes(ctx, store, testScheduleNamespace, "nas")
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("ListOutcomes = %+v, want exactly one recorded fire", outs)
	}
	if outs[0].Success || outs[0].Snapshot != "" {
		t.Fatalf("outcome = %+v, want a failed fire with no snapshot (no elevation bypass)", outs[0])
	}
	if outs[0].ErrorText != ErrElevationRequired.Error() {
		t.Fatalf("outcome.ErrorText = %q, want the elevation-required refusal specifically (%q) -- "+
			"any other failure reason would mean the unattended path is not provably blocked by the "+
			"elevation gate itself", outs[0].ErrorText, ErrElevationRequired.Error())
	}
}

// TestMultiTargetPolicy proves a due fire selects EXACTLY the policy's own
// target: two targets, each with its own scheduled job, and one Tick after
// only one target's window has elapsed fires that target alone, leaving
// the other's outcome history empty.
func TestMultiTargetPolicy(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)

	fast := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	fastPolicy := TargetPolicy{Target: "nas", CronSpec: "@every 1h", Domains: []string{"context"}}
	slow := TargetRecord{Name: "b2", Kind: TargetKindFS, FSRoot: t.TempDir()}
	slowPolicy := TargetPolicy{Target: "b2", CronSpec: "@every 24h", Domains: []string{"context"}}

	if err := RegisterBackupJob(ctx, sched, store, testScheduleNamespace, fast, fastPolicy, clock, nil, nil); err != nil {
		t.Fatalf("RegisterBackupJob(nas): %v", err)
	}
	if err := RegisterBackupJob(ctx, sched, store, testScheduleNamespace, slow, slowPolicy, clock, nil, nil); err != nil {
		t.Fatalf("RegisterBackupJob(b2): %v", err)
	}
	if _, err := sched.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close(context.Background()) })

	clock.Advance(2 * time.Hour) // past nas's window, well short of b2's
	if _, err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	nasOuts, err := ListOutcomes(ctx, store, testScheduleNamespace, "nas")
	if err != nil {
		t.Fatalf("ListOutcomes(nas): %v", err)
	}
	if len(nasOuts) != 1 {
		t.Fatalf("ListOutcomes(nas) = %+v, want exactly one fire", nasOuts)
	}
	b2Outs, err := ListOutcomes(ctx, store, testScheduleNamespace, "b2")
	if err != nil {
		t.Fatalf("ListOutcomes(b2): %v", err)
	}
	if len(b2Outs) != 0 {
		t.Fatalf("ListOutcomes(b2) = %+v, want none -- its 24h window has not elapsed", b2Outs)
	}
}

// TestRegisterConfiguredBackupJobs proves the composition-root entry
// point: it lists every persisted target/policy pair, registers each as a
// scheduled job, and reports (never silently drops) a target with no
// matching policy.
func TestRegisterConfiguredBackupJobs(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)

	nas := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	if err := PutTarget(ctx, store, testScheduleNamespace, nas); err != nil {
		t.Fatalf("PutTarget(nas): %v", err)
	}
	nasPolicy := TargetPolicy{Target: "nas", CronSpec: "@every 6h", Domains: []string{"context"}}
	if err := PutPolicy(ctx, store, testScheduleNamespace, nasPolicy); err != nil {
		t.Fatalf("PutPolicy(nas): %v", err)
	}
	// "orphan" has a target but no matching policy -- must be reported as
	// skipped, never silently dropped.
	orphan := TargetRecord{Name: "orphan", Kind: TargetKindFS, FSRoot: t.TempDir()}
	if err := PutTarget(ctx, store, testScheduleNamespace, orphan); err != nil {
		t.Fatalf("PutTarget(orphan): %v", err)
	}

	skipped, err := RegisterConfiguredBackupJobs(ctx, sched, store, testScheduleNamespace, clock, nil, nil)
	if err != nil {
		t.Fatalf("RegisterConfiguredBackupJobs: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != "orphan" {
		t.Fatalf("RegisterConfiguredBackupJobs skipped = %v, want exactly [orphan]", skipped)
	}

	report, err := sched.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(report.Scheduled) != 1 || report.Scheduled[0].Owner != backupJobOwner("nas") {
		t.Fatalf("Activate report.Scheduled = %+v, want exactly the nas job", report.Scheduled)
	}
}

// TestBackupJobSkipMissed inherits C/S-04.T4's skip-missed semantics
// verbatim: a daemon down across many scheduled windows fires exactly
// once for the next valid window, never once per missed window.
func TestBackupJobSkipMissed(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)
	target := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	pol := TargetPolicy{Target: "nas", CronSpec: "@every 1h", Domains: []string{"context"}}

	if err := RegisterBackupJob(ctx, sched, store, testScheduleNamespace, target, pol, clock, nil, nil); err != nil {
		t.Fatalf("RegisterBackupJob: %v", err)
	}
	if _, err := sched.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close(context.Background()) })

	// Simulate a long outage: 30 missed hourly windows in one jump.
	clock.Advance(30 * time.Hour)
	report, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(report.Fired) != 1 {
		t.Fatalf("Tick report.Fired = %+v, want exactly one fire after a long outage (skip-missed), not 30", report.Fired)
	}
	outs, err := ListOutcomes(ctx, store, testScheduleNamespace, "nas")
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("ListOutcomes = %+v, want exactly one recorded outcome, not one per missed window", outs)
	}
}
