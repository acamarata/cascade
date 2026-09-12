package daemon

// Purpose: the merge-fix companion to subsystems_worktree.go: AC/S-59.T5's
//
//	internal/jobs.Scheduler shipped with no production caller anywhere in
//	this tree (internal/build's test-only gate flagged NewScheduler,
//	Guard, DecodeEvent alongside it). RegisterScheduler is that caller —
//	the daemon composition root's call site, same treatment
//	RegisterWorktreeSweep/RegisterWorktreeManager give AC/S-59.T3's
//	WorktreeManager immediately above in subsystems.go.
//
// Inputs: a real *events.Bus, this daemon's runtime.Clock, a
//
//	nodes.ControllerBindingBackend (nil is nodes.ResolveRole's own
//	documented safe default: a single-node/not-yet-enrolled daemon
//	resolves to RoleController), a cursor name for the bus subscription,
//	and (DEFECT-scheduler-resume-never-called's fix) the real *jobs.Store
//	and journal.Store Resume needs to re-enter stale `running` jobs at
//	`leased` on startup, plus the heartbeat reaper's interval.
//
// Outputs: a constructed *jobs.Scheduler and a live background goroutine
//
//	(runSchedulerConsumer, subsystems_scheduler_events.go) consuming
//	jobs.LeaseEventNamespace until ctx is canceled; Manifest records
//	Started/Failed exactly as every other Register* call here does.
//
// Constraints: resolves this daemon's nodes.Role ONCE, at registration
//
//	time, via nodes.ResolveRole, and carries it on the consumer's context
//	via nodes.WithRole -- the real R-21.169 controller-singleton posture
//	scheduler_controller.go's Guard/requireControllerCtx already enforces
//	on every Advance call. A node-role daemon reaching this composition
//	root now genuinely refuses via a typed error, rather than a call
//	nothing in the tree could ever reach.
//
// RESUME POLICY (DEFECT-scheduler-resume-never-called): Resume runs
// synchronously, once, BEFORE the consumer goroutine starts -- the same
// "before this subsystem serves anything new" position wireResumeScan
// (cmd/cascade/daemon_resume.go) already uses for the fleet resume scan.
// Two outcomes:
//  1. Resume refuses with a PermissionDenied-shaped error because this
//     daemon is not the controller (R-21.169). This is EXPECTED and
//     routine on a node daemon -- Advance refuses the identical way on
//     every lease event this same daemon will otherwise consume -- so it
//     is not fatal: RegisterScheduler logs the skip in its Started detail
//     and the subsystem still starts.
//  2. Resume fails for any OTHER reason (a store I/O error, a journal
//     replay failure). This IS fatal: RegisterScheduler records Failed
//     and returns the error, refusing to start the subsystem, matching
//     wireResumeScan's own refuse-to-start precedent for the identical
//     class of problem (a startup-time crash-recovery scan that cannot
//     run). The alternative -- log and continue -- would silently
//     reproduce a milder version of the exact defect this fix closes:
//     jobs left `running` forever with nobody told the reason. A loud
//     startup failure is the honest outcome; a daemon that cannot recover
//     its own job state should not silently pretend to be healthy.
//
// CONTRACT DEVIATION (disclosed, matching RegisterConductorRouter/
// RegisterReachability/RegisterWorktreeSweep's own precedent in
// subsystems.go): the consumer always calls Advance against an empty
// jobs.ExecutionDag{} and an empty jobStates map -- no persisted per-repo
// DAG exists in this tree yet (AC/S-59.T4's planner output has no store a
// daemon composition root can read from today), so admitNodes never
// admits a node and governorFn is never invoked (nil is safe here; see
// scheduler_admit.go's admitNodes, which only calls governorFn once per
// ADMISSIBLE node). The CancelRequested/TerminationConfirmed/
// LeaseReclaimed input variants, and therefore RecordIntent/MarkEffect's
// own "same transaction as the paired StateTransition" contract, need a
// real coordinator over a persisted DAG and job-state store that does not
// exist in this tree yet (AC/S-60.T1's RPC surface, AD/S-61.T1's
// termination driver) -- see internal/build/testonly-allow.json's own
// entries for jobs.RecordIntent and jobs.MarkEffect, which name those
// forward tickets rather than papering over the gap with fabricated
// call sites.
//
// SPORT: internal/daemon (ADD, merge-fix for P1-E29-W6-S59-T5).

import (
	"context"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// jobSchedulerSubsystem is the fail-loud Manifest name RegisterScheduler
// reports under (R-14.87).
const jobSchedulerSubsystem = "jobs.scheduler"

// schedulerJournalReader adapts a real journal.Store (RegisterWorktreeSweep's
// own `j journal.Store` parameter carries the identical type, immediately
// above in subsystems.go) onto jobs.Reader -- the boundary internal/jobs'
// own doc.go names in so many words ("no direct import of
// internal/fleet/journal from this package; the caller wires it to the
// real M/S-27.T1 store"). internal/daemon already imports both packages,
// so this is the existing composition-root seam, not a new import edge.
type schedulerJournalReader struct{ store journal.Store }

// Replay implements jobs.Reader over the real journal.Store.Replay,
// translating journal.Entry/Cursor into jobs.JournalEntry/int64 the way
// Resume's Reader seam expects (scheduler.go's own doc comment names this
// exact translation as the caller's job).
func (r schedulerJournalReader) Replay(ctx context.Context, entityID string, cursor int64) ([]jobs.JournalEntry, error) {
	entries, err := r.store.Replay(ctx, entityID, journal.Cursor{EntityID: entityID, Seq: uint64(cursor)}, nil)
	if err != nil {
		return nil, err
	}
	out := make([]jobs.JournalEntry, len(entries))
	for i, e := range entries {
		out[i] = jobs.JournalEntry{
			EntityID: e.EntityID,
			Cursor:   int64(e.Seq),
			Kind:     e.Kind.String(),
			Payload:  []byte(e.Payload),
		}
	}
	return out, nil
}

// RegisterScheduler resolves this daemon's controller Role, constructs the
// real jobs.Scheduler, runs Resume once against store/j (see this file's
// RESUME POLICY doc comment above for the two outcomes), then subscribes
// to the jobs.lease bus namespace under cursorName and starts the
// consumer as its own goroutine bounded by ctx's cancellation -- there is
// no separate Stop call, matching RegisterWorktreeManager's own posture
// immediately above. A nil store skips Resume entirely (no data
// dependency to synthesize, matching RegisterRecallIndexHandler's own
// "a nil store leaves the namespace unregistered" precedent) -- every
// production call site wires a real one. A Subscribe failure is recorded
// as Failed and returned; the consumer's own terminal error, if any, is
// recorded as Failed asynchronously since it only returns after ctx ends
// or the subscription's channel closes.
func (m *Manifest) RegisterScheduler(ctx context.Context, bus *events.Bus, clock runtime.Clock, binding nodes.ControllerBindingBackend, cursorName string, store *jobs.Store, j journal.Store, heartbeatInterval time.Duration) (*jobs.Scheduler, error) {
	m.Register(jobSchedulerSubsystem)
	role, err := nodes.ResolveRole(binding)
	if err != nil {
		m.Failed(jobSchedulerSubsystem, err.Error())
		return nil, err
	}
	roleCtx := nodes.WithRole(ctx, role)
	sched := jobs.NewScheduler(clock)

	resumeDetail, err := runSchedulerResume(roleCtx, sched, store, j, heartbeatInterval)
	if err != nil {
		m.Failed(jobSchedulerSubsystem, err.Error())
		return nil, err
	}

	sub, err := bus.Subscribe(ctx, jobs.LeaseEventNamespace, cursorName, 64)
	if err != nil {
		m.Failed(jobSchedulerSubsystem, err.Error())
		return nil, err
	}
	go func() {
		if runErr := runSchedulerConsumer(roleCtx, sched, sub); runErr != nil {
			m.Failed(jobSchedulerSubsystem, runErr.Error())
		}
	}()
	m.Started(jobSchedulerSubsystem, fmt.Sprintf("role=%s subscribed cursor %q on namespace %q (%s)", role, cursorName, jobs.LeaseEventNamespace, resumeDetail))
	return sched, nil
}

// runSchedulerResume applies this file's RESUME POLICY: a nil store skips
// Resume (detail says so); a PermissionDenied refusal (non-controller) is
// swallowed and reported in the detail string, never as an error; any
// other error is returned for the caller to treat as fatal.
func runSchedulerResume(roleCtx context.Context, sched *jobs.Scheduler, store *jobs.Store, j journal.Store, heartbeatInterval time.Duration) (string, error) {
	if store == nil {
		return "resume skipped: no store wired", nil
	}
	var reader jobs.Reader
	if j != nil {
		reader = schedulerJournalReader{store: j}
	}
	if _, err := sched.Resume(roleCtx, store, reader, heartbeatInterval, nil, nil); err != nil {
		if cascade.HasKind(err, cascade.KindPermissionDenied) {
			return "resume skipped: not controller", nil
		}
		return "", err
	}
	return "resume ran", nil
}
