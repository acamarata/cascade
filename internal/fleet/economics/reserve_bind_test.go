package economics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

var testPlacement = Placement{NodeID: "node-1", SelectedTier: "tier-a", Sensitivity: "internal", DecisionID: "dec-1"}

// bindFixture reserves one interactive row on a store that also carries
// a caller-owned table (the scheduler-decision stand-in the BindFn
// writes inside the same transaction).
func bindFixture(t *testing.T) (*reserverFixture, *Reserver, Reservation) {
	t.Helper()
	f := newReserverFixture(t, 100000)
	// Two connections, as a daemon pool has: a transaction left open by a
	// failed Bind then blocks the rollback write instead of sharing it.
	f.store.DB().SetMaxOpenConns(2)
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TABLE caller_decision (execution_id TEXT PRIMARY KEY, node_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create caller table: %v", err)
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return f, rv, r
}

// writeCallerRow is the BindFn: it writes the caller's row on the tx.
func writeCallerRow(ctx context.Context, tx *sql.Tx, r Reservation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO caller_decision (execution_id, node_id) VALUES (?, ?)`, r.ExecutionID, r.NodeID)
	return err
}

// requireBindRolledBack asserts no caller row, empty placement columns
// and a rolled_back row whose every acquired step was compensated.
func requireBindRolledBack(t *testing.T, f *reserverFixture, r Reservation, err error, causes ...error) {
	t.Helper()
	matched := false
	for _, c := range causes {
		matched = matched || hasIdentity(err, c) || (err != nil && strings.Contains(err.Error(), c.Error()))
	}
	if !matched {
		t.Fatalf("Bind err = %v, want one of the causes %v", err, causes)
	}
	var n int
	if qerr := f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM caller_decision`).Scan(&n); qerr != nil || n != 0 {
		t.Fatalf("caller rows = %d (%v), want 0 after a failed Bind", n, qerr)
	}
	got, _, _ := f.store.Get(t.Context(), r.ID)
	if got.NodeID != "" || got.SelectedTier != "" || got.Sensitivity != "" || got.DecisionID != "" {
		t.Fatalf("placement columns after a failed Bind = %+v, want unchanged (empty)", got)
	}
	if got.State != ReservationRolledBack || findStep(got, StepLeases).State != StepCompensated || findStep(got, StepWorktree).State != StepCompensated {
		t.Fatalf("row after a failed Bind = %+v, want rolled_back with leases and worktree compensated", got)
	}
}

// TestBindCommitsPlacementAndCallerRowsAtomically: Bind writes the
// placement and runs the BindFn in ONE tx; a BindFn error, an injected
// placement-write error and a commit error each roll the tx back and
// then run the reverse teardown (one subtest each, on a fresh fixture);
// Bind on a non-held row refuses.
func TestBindCommitsPlacementAndCallerRowsAtomically(t *testing.T) {
	t.Run("commits_and_refuses_non_held", bindCommitsAndRefusesNonHeld)
	t.Run("bind_fn_error_rolls_back", bindFnErrorRollsBack)
	t.Run("placement_write_error_rolls_back", bindPlacementWriteErrorRollsBack)
	t.Run("commit_error_rolls_back", bindCommitErrorRollsBack)
}

func bindCommitsAndRefusesNonHeld(t *testing.T) {
	f, rv, r := bindFixture(t)
	bound, err := rv.Bind(t.Context(), r.ID, testPlacement, writeCallerRow)
	if err != nil || bound.NodeID != "node-1" || bound.State != ReservationHeld {
		t.Fatalf("Bind = %+v, %v, want placement on a held row", bound, err)
	}
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	var node string
	if qerr := f.store.DB().QueryRowContext(t.Context(), `SELECT node_id FROM caller_decision WHERE execution_id = ?`, r.ExecutionID).Scan(&node); qerr != nil || node != "node-1" {
		t.Fatalf("caller row node = %q (%v), want node-1 written in the Bind tx", node, qerr)
	}
	if got := (Placement{stored.NodeID, stored.SelectedTier, stored.Sensitivity, stored.DecisionID}); got != testPlacement {
		t.Fatalf("stored placement = %+v", stored)
	}
	if _, err := rv.Commit(t.Context(), r.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_, err = rv.Bind(t.Context(), r.ID, testPlacement, writeCallerRow)
	requireSentinel(t, err, ErrReservationTransition, "committed")
	if after, _, _ := f.store.Get(t.Context(), r.ID); after.State != ReservationCommitted || after.WorktreeID == "" {
		t.Fatalf("refused Bind changed the committed row: %+v", after)
	}
	if _, err := rv.Bind(t.Context(), r.ID, Placement{NodeID: "n"}, writeCallerRow); !isKindInvalidInput(err) {
		t.Errorf("Bind with an incomplete placement = %v, want KindInvalidInput", err)
	}
}

func bindFnErrorRollsBack(t *testing.T) {
	f, rv, r := bindFixture(t)
	cause := errors.New("scheduler decision refused")
	_, err := rv.Bind(t.Context(), r.ID, testPlacement, func(ctx context.Context, tx *sql.Tx, r Reservation) error {
		if werr := writeCallerRow(ctx, tx, r); werr != nil {
			return werr
		}
		return cause
	})
	requireBindRolledBack(t, f, r, err, cause)
}

func bindPlacementWriteErrorRollsBack(t *testing.T) {
	f, rv, r := bindFixture(t)
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_placement BEFORE UPDATE OF node_id ON jobs_reservation
		WHEN NEW.node_id = 'node-1' BEGIN SELECT RAISE(ABORT, 'injected placement write failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	ran := false
	_, err := rv.Bind(t.Context(), r.ID, testPlacement, func(context.Context, *sql.Tx, Reservation) error { ran = true; return nil })
	if ran {
		t.Error("BindFn ran after the placement write failed")
	}
	requireBindRolledBack(t, f, r, err, errors.New("injected placement write failure"))
}

func bindCommitErrorRollsBack(t *testing.T) {
	f, rv, r := bindFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	_, err := rv.Bind(ctx, r.ID, testPlacement, func(ctx context.Context, tx *sql.Tx, r Reservation) error {
		if werr := writeCallerRow(ctx, tx, r); werr != nil {
			return werr
		}
		cancel() // the caller's context ends between the BindFn and the commit
		return nil
	})
	requireBindRolledBack(t, f, r, err, context.Canceled, sql.ErrTxDone)
}

// stressLog collects what the stress goroutines saw.
type stressLog struct {
	mu    sync.Mutex
	bound map[string]string
	errs  []error
}

func (l *stressLog) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, err)
}

func (l *stressLog) bind(id, decision string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bound[id] = decision
}

// stressWorker reserves, binds and then commits or releases six rows. A
// Bind refused because a cascade parked the row first is expected.
func stressWorker(t *testing.T, rv *Reserver, w int, log *stressLog) {
	for i := range 6 {
		r, err := rv.Reserve(t.Context(), baseReserveRequest())
		if err != nil {
			log.fail(fmt.Errorf("Reserve: %w", err))
			continue
		}
		p := testPlacement
		p.DecisionID = fmt.Sprintf("dec-%d-%d", w, i)
		if _, err := rv.Bind(t.Context(), r.ID, p, writeCallerRow); err == nil {
			log.bind(r.ID, p.DecisionID)
		} else if !hasIdentity(err, ErrReservationTransition) {
			log.fail(fmt.Errorf("Bind: %w", err))
		}
		if i%2 == 0 {
			_, _ = rv.Commit(t.Context(), r.ID) // a parked row refuses; either is fine
		} else if _, err := rv.Release(t.Context(), r.ID); err != nil {
			log.fail(fmt.Errorf("Release: %w", err))
		}
	}
}

// stressTick runs one cascade, one ExpireStale and one Heartbeat.
func stressTick(t *testing.T, rv *Reserver, log *stressLog) {
	if _, err := rv.CascadeRateLimited(t.Context(), "scope-1"); err != nil {
		log.fail(fmt.Errorf("cascade: %w", err))
	}
	if _, err := rv.ExpireStale(t.Context(), hbInterval); err != nil {
		log.fail(fmt.Errorf("ExpireStale: %w", err))
	}
	if _, _, err := rv.Heartbeat(t.Context()); err != nil {
		log.fail(fmt.Errorf("Heartbeat: %w", err))
	}
}

// TestReserveBindCascadeExpireStress races Reserve, Bind, Commit and
// Release workers against cascade, ExpireStale and Heartbeat loops on one
// Reserver. Every Bind that committed keeps its placement and caller row,
// no in-flight mark survives, and every stale unheld row is retired.
func TestReserveBindCascadeExpireStress(t *testing.T) {
	f, rv, _ := bindFixture(t)
	f.store.DB().SetMaxOpenConns(1) // one writer, as SQLite allows; no busy retries
	f.capacity = 1 << 40            // room for every worker's rows; quota is not under test here
	for i := range 8 {
		insertUnheld(t, f, fmt.Sprintf("r-stale-%d", i), ReservationCommitted, 0)
	}
	log := &stressLog{bound: map[string]string{}}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() { stressWorker(t, rv, w, log) })
	}
	for range 24 {
		wg.Go(func() { stressTick(t, rv, log) })
	}
	wg.Wait()
	if len(log.errs) != 0 || len(log.bound) == 0 {
		t.Fatalf("errors under contention: %v; binds landed: %d (want none and some)", log.errs, len(log.bound))
	}
	for id, dec := range log.bound {
		got, _, _ := f.store.Get(t.Context(), id)
		var n int
		_ = f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM caller_decision WHERE execution_id = ?`, got.ExecutionID).Scan(&n)
		if got.DecisionID != dec || got.NodeID != testPlacement.NodeID || n != 1 {
			t.Errorf("%s = %+v caller rows %d, want decision %s kept", id, got, n, dec)
		}
	}
	rv.acqMu.Lock()
	inflight := len(rv.acquiring)
	rv.acqMu.Unlock()
	if inflight != 0 {
		t.Errorf("acquiring holds %d ids after the stress, want 0", inflight)
	}
	for i := range 8 {
		if s, _, _ := f.store.Get(t.Context(), fmt.Sprintf("r-stale-%d", i)); s.State != ReservationReleased {
			t.Errorf("r-stale-%d = %s, want released", i, s.State)
		}
	}
}
