package economics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func notLive(context.Context) (bool, error) { return false, nil }

// allRows reads every reservation row, ordered by id, for before/after
// comparisons.
func allRows(t *testing.T, s *ReservationStore) string {
	t.Helper()
	var out []string
	for _, st := range []ReservationState{ReservationHeld, ReservationParked, ReservationCommitted, ReservationReleased, ReservationRolledBack} {
		rows, err := s.ListByState(t.Context(), st)
		if err != nil {
			t.Fatalf("ListByState: %v", err)
		}
		for _, r := range rows {
			out = append(out, fmt.Sprint(r))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// seedForeignRows writes the rows a dead daemon (epoch-dead) left.
func seedForeignRows(t *testing.T, f *reserverFixture) {
	t.Helper()
	now := f.clock.Now().Unix()
	insertUnheld(t, f, "r-held", ReservationHeld, now)
	insertUnheld(t, f, "r-parked", ReservationParked, now)
	insertUnheld(t, f, "r-commit", ReservationCommitted, now)
	pending := baseReservation("r-pending")
	pending.OwnerEpoch = "epoch-dead"
	pending.Steps = []Step{{Step: StepQuota, IdempotencyKey: "r-pending:quota", State: StepAcquired}, {Step: StepLeases, IdempotencyKey: "r-pending:leases", State: StepPending}}
	batch := baseReservation("r-batch")
	batch.Kind, batch.ScopeGlobs, batch.ExpiresAt, batch.OwnerEpoch = ReservationBatch, nil, now-1, "epoch-dead"
	for _, r := range []Reservation{pending, batch} {
		if _, err := f.store.Insert(t.Context(), r); err != nil {
			t.Fatalf("Insert(%s): %v", r.ID, err)
		}
	}
}

// TestReserveSweepStart: with no live other daemon, Sweep recovers and
// rolls back every foreign held/parked row (reverse compensation),
// adopts every foreign committed row without holding it in memory,
// rolls back an expired batch row, leaves this epoch's rows alone and
// is idempotent.
func TestReserveSweepStart(t *testing.T) {
	f := newReserverFixture(t, 100000)
	var released []string
	f.seams.ReleaseLeases = func(_ context.Context, ids []string) error {
		released = append(released, ids...)
		f.rec.record("release_leases")
		return nil
	}
	f.seams.FindLeases = func(_ context.Context, repo, job string, globs []string) ([]string, error) {
		return []string{"found-" + repo + "-" + job + "-" + strings.Join(globs, ",")}, nil
	}
	rv := f.reserver(t)
	own, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	seedForeignRows(t, f)
	f.rec.logs, released = nil, nil
	rep, err := rv.Sweep(t.Context(), notLive)
	if err != nil || rep != (SweepReport{RolledBack: 3, Adopted: 1, Expired: 1}) {
		t.Fatalf("Sweep = %+v, %v, want 3 rolled back, 1 adopted, 1 expired", rep, err)
	}
	want := map[string]ReservationState{"r-held": ReservationRolledBack, "r-parked": ReservationRolledBack, "r-pending": ReservationRolledBack,
		"r-batch": ReservationRolledBack, "r-commit": ReservationCommitted, own.ID: ReservationHeld}
	for id, st := range want {
		if r, _, _ := f.store.Get(t.Context(), id); r.State != st {
			t.Errorf("%s = %s, want %s", id, r.State, st)
		}
	}
	if adopted, _, _ := f.store.Get(t.Context(), "r-commit"); adopted.OwnerEpoch != "epoch-1" {
		t.Errorf("adopted owner_epoch = %q, want epoch-1", adopted.OwnerEpoch)
	}
	sort.Strings(released)
	if fmt.Sprint(released) != "[found-repo-1-job-1-src/** r-held-lease r-parked-lease]" {
		t.Errorf("released = %v, want the recovered lease and both foreign leases", released)
	}
	if got := fmt.Sprint(f.rec.snapshot()); strings.Count(got, "remove_worktree release_leases") != 2 {
		t.Errorf("compensation order %s, want worktree before leases for both foreign rows", got)
	}
	if renewed, _, _ := rv.Heartbeat(t.Context()); renewed != 1 {
		t.Errorf("Heartbeat renewed %d, want only this epoch's own row (the adopted row is not held)", renewed)
	}
	if again, err := rv.Sweep(t.Context(), notLive); err != nil || again != (SweepReport{}) {
		t.Errorf("second Sweep = %+v, %v, want zero", again, err)
	}
}

// TestReserveSweepRefusesConcurrentDaemon: a live other daemon on the
// same home refuses the sweep with ErrConcurrentDaemon and writes
// nothing; a failing liveness probe is returned and writes nothing.
func TestReserveSweepRefusesConcurrentDaemon(t *testing.T) {
	f := newReserverFixture(t, 100000)
	seedForeignRows(t, f)
	rv := f.reserverWithEpoch(t, "epoch-new")
	before := allRows(t, f.store)
	rep, err := rv.Sweep(t.Context(), func(context.Context) (bool, error) { return true, nil })
	requireSentinel(t, err, ErrConcurrentDaemon)
	probe := errors.New("home lock probe failed")
	_, perr := rv.Sweep(t.Context(), func(context.Context) (bool, error) { return false, probe })
	if !hasIdentity(perr, probe) {
		t.Errorf("Sweep with a failing probe = %v, want the probe error", perr)
	}
	if rep != (SweepReport{}) || allRows(t, f.store) != before || len(f.rec.snapshot()) != 0 {
		t.Fatalf("refused Sweep wrote: report %+v, calls %v", rep, f.rec.snapshot())
	}
	if _, err := rv.Sweep(t.Context(), nil); !isKindInvalidInput(err) {
		t.Errorf("Sweep(nil probe) = %v, want KindInvalidInput", err)
	}
}

// TestExpireStaleSkipsOwnRowsAfterClockJump: after the clock jumps 4 x
// interval with no Heartbeat, ExpireStale leaves every row this Reserver
// holds untouched and retires only the unheld stale row.
func TestExpireStaleSkipsOwnRowsAfterClockJump(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	a, errA := rv.Reserve(t.Context(), baseReserveRequest())
	b, errB := rv.Reserve(t.Context(), baseReserveRequest())
	if errA != nil || errB != nil {
		t.Fatalf("Reserve: %v %v", errA, errB)
	}
	if _, err := rv.Commit(t.Context(), b.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	insertUnheld(t, f, "r-stale", ReservationHeld, f.clock.Now().Unix())
	f.clock.Advance(4 * hbInterval)
	f.rec.logs = nil
	if _, err := rv.ExpireStale(t.Context(), 0); !isKindInvalidInput(err) {
		t.Fatalf("ExpireStale(0) = %v, want KindInvalidInput", err)
	}
	n, err := rv.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 1 {
		t.Fatalf("ExpireStale = %d, %v, want exactly the unheld row", n, err)
	}
	for id, want := range map[string]ReservationState{a.ID: ReservationHeld, b.ID: ReservationCommitted, "r-stale": ReservationRolledBack} {
		if r, _, _ := f.store.Get(t.Context(), id); r.State != want {
			t.Errorf("%s = %s, want %s", id, r.State, want)
		}
	}
	if got := fmt.Sprint(f.rec.snapshot()); got != "[remove_worktree release_leases]" {
		t.Errorf("compensations = %s, want only the unheld row's", got)
	}
}

// TestReserveKill9AdoptionRecovers: a child holding one held and one
// committed reservation with a repo lease is SIGKILLed; a new Reserver's
// Sweep rolls the held row back at once and adopts the committed one;
// after 3 intervals on the injected clock ExpireStale releases it; no
// row of the dead epoch stays active and no lease stays live.
func TestReserveKill9AdoptionRecovers(t *testing.T) {
	dir := t.TempDir()
	dbPath, fakePath := filepath.Join(dir, "reservation.db"), filepath.Join(dir, "subsystems.json")
	clock := newStepClock()
	store, err := NewReservationStore(openReservationTestDBAt(t, dbPath), clock)
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	spawnAndKillReserver(t, dbPath, fakePath)
	fake := &durableFake{path: fakePath}
	if st := fake.update(func(*fakeState) {}); len(st.Leases) != 2 {
		t.Fatalf("child leases = %+v, want two live leases before the kill", st.Leases)
	}
	rv, err := NewReserver(store, clock, "epoch-parent", fake.seams())
	if err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	clock.Advance(hbInterval / 2)
	if rep, err := rv.Sweep(t.Context(), notLive); err != nil || rep.RolledBack != 1 || rep.Adopted != 1 {
		t.Fatalf("Sweep = %+v, %v, want 1 rolled back and 1 adopted", rep, err)
	}
	if n, err := rv.ExpireStale(t.Context(), hbInterval); err != nil || n != 0 {
		t.Fatalf("ExpireStale before 3 intervals = %d, %v, want 0", n, err)
	}
	clock.Advance(3*hbInterval + time.Second)
	if n, err := rv.ExpireStale(t.Context(), hbInterval); err != nil || n != 1 {
		t.Fatalf("ExpireStale after 3 intervals = %d, %v, want the adopted row released", n, err)
	}
	for _, st := range []ReservationState{ReservationHeld, ReservationParked, ReservationCommitted} {
		if rows, _ := store.ListByState(t.Context(), st); len(rows) != 0 {
			t.Errorf("%d %s rows remain, want none", len(rows), st)
		}
	}
	for id, l := range fake.update(func(*fakeState) {}).Leases {
		if l.Live {
			t.Errorf("lease %s still live", id)
		}
	}
}

// spawnAndKillReserver starts the kill9 child, waits for READY and
// SIGKILLs it. The child's stdin is a held-open pipe so it is still
// blocked when the kill lands, on every platform.
func spawnAndKillReserver(t *testing.T, dbPath, fakePath string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReserveCrashHelperProcess$")
	cmd.Env = append(os.Environ(), crashDBEnv+"="+dbPath, crashFakeEnv+"="+fakePath, crashSiteEnv+"=kill9")
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = stdinW.Close() }()
	cmd.Stdin = stdinR
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	_ = stdinR.Close()
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "READY\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child: want READY, got %q", line)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()
}

// TestExpireStaleYieldsToAnotherProcess: two Reservers on one database.
// B adopted r-a, r-b and r-c by Sweep and holds none in memory. While A's
// ExpireStale tears r-a down, B adopts r-a and r-b, and a third process
// claims r-c (owner_epoch rewritten, heartbeat unchanged). A fenced r-a
// before its teardown, so B's Adopt of it is refused; r-b and r-c changed
// after A listed them, so A skips both: committed, worktree intact.
func TestExpireStaleYieldsToAnotherProcess(t *testing.T) {
	f := newReserverFixture(t, 100000)
	for _, id := range []string{"r-a", "r-b", "r-c"} {
		insertUnheld(t, f, id, ReservationCommitted, f.clock.Now().Unix())
	}
	b := f.reserverWithEpoch(t, "epoch-B")
	if rep, err := b.Sweep(t.Context(), notLive); err != nil || rep.Adopted != 3 {
		t.Fatalf("B.Sweep = %+v, %v, want three rows adopted", rep, err)
	}
	var adoptA, adoptB error
	f.seams.RemoveWorktree = func(ctx context.Context, wt string) error {
		if wt == "r-a-wt" {
			adoptA, adoptB = b.Adopt(ctx, "r-a"), b.Adopt(ctx, "r-b")
			_, err := f.store.DB().ExecContext(ctx, `UPDATE jobs_reservation SET owner_epoch = 'epoch-C' WHERE id = 'r-c'`)
			return err
		}
		return nil
	}
	a := f.reserverWithEpoch(t, "epoch-A")
	f.clock.Advance(4 * hbInterval)
	n, err := a.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 1 {
		t.Fatalf("A.ExpireStale = %d, %v, want exactly r-a retired", n, err)
	}
	if !cascade.HasKind(adoptA, cascade.KindConflict) || b.isTracked("r-a") {
		t.Fatalf("B.Adopt(r-a) during A's teardown = %v tracked=%v, want refused", adoptA, b.isTracked("r-a"))
	}
	if s, _, _ := f.store.Get(t.Context(), "r-a"); s.State != ReservationReleased {
		t.Fatalf("r-a = %s, want released", s.State)
	}
	if adoptB != nil || !b.isTracked("r-b") {
		t.Fatalf("B.Adopt(r-b) = %v, want nil and tracked by B", adoptB)
	}
	for id, epoch := range map[string]string{"r-b": "epoch-B", "r-c": "epoch-C"} {
		if s, _, _ := f.store.Get(t.Context(), id); s.State != ReservationCommitted || s.OwnerEpoch != epoch || s.WorktreeID != id+"-wt" {
			t.Errorf("%s = %+v, want committed, owned by %s, worktree intact", id, s, epoch)
		}
	}
}
