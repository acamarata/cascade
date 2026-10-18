// Purpose: RepoOfLease over real SQLite: the repo a caller put on
// DispatchRef comes back for a live lease only, and every other case fails
// closed.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func repoDispatch(repo string) DispatchRef {
	d := dispatchFor("job-1")
	d.RepoID = repo
	return d
}

// requireUnknown fails unless RepoOfLease answers ErrLeaseUnknown for id.
func requireUnknown(t *testing.T, f *fx, id, why string) {
	t.Helper()
	repo, ok, err := f.fleet.RepoOfLease(t.Context(), id)
	if repo != "" || ok || err == nil {
		t.Fatalf("%s: RepoOfLease = (%q, %v, %v), want (\"\", false, ErrLeaseUnknown)", why, repo, ok, err)
	}
	requireSentinel(t, err, ErrLeaseUnknown, "lease")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("%s: err = %v, want KindNotFound", why, err)
	}
}

func TestFleetRepoOfLease(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	a, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil || a.RepoID != "repo-a" {
		t.Fatalf("Lease(repo-a) = (%+v, %v), want Lease.RepoID repo-a", a, err)
	}
	b, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-b"), 1)
	if err != nil {
		t.Fatalf("Lease(repo-b): %v", err)
	}
	none, err := f.fleet.Lease(t.Context(), request(), repoDispatch(""), 1)
	if err != nil {
		t.Fatalf("Lease(no repo): %v", err)
	}
	for _, c := range []struct {
		l    Lease
		repo string
		ok   bool
	}{{a, "repo-a", true}, {b, "repo-b", true}, {none, "", false}} {
		if repo, ok, err := f.fleet.RepoOfLease(t.Context(), c.l.ID); repo != c.repo || ok != c.ok || err != nil {
			t.Fatalf("RepoOfLease(%s) = (%q, %v, %v), want (%q, %v, nil)", c.l.RepoID, repo, ok, err, c.repo, c.ok)
		}
	}
	if err := f.fleet.Commit(t.Context(), a); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if repo, ok, err := f.fleet.RepoOfLease(t.Context(), a.ID); repo != "repo-a" || !ok || err != nil {
		t.Fatalf("RepoOfLease(committed) = (%q, %v, %v), want repo-a", repo, ok, err)
	}

	requireUnknown(t, f, "lease-never-issued", "never issued")
	if _, _, err := f.fleet.RepoOfLease(t.Context(), ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("RepoOfLease(\"\") = %v, want KindInvalidInput", err)
	}
	if err := f.fleet.Release(t.Context(), b); err != nil {
		t.Fatalf("Release: %v", err)
	}
	requireDropped(t, f, b.ID, "Release")
	requireUnknown(t, f, b.ID, "after Release")
	if repo, ok, err := f.fleet.RepoOfLease(t.Context(), a.ID); repo != "repo-a" || !ok || err != nil {
		t.Fatalf("RepoOfLease(repo-a) after releasing repo-b = (%q, %v, %v), want no cross-read", repo, ok, err)
	}
	t.Run("fails closed", repoOfLeaseFailsClosed)
}

// requireDropped fails if the lease index still holds id after op returned nil.
func requireDropped(t *testing.T, f *fx, id, op string) {
	t.Helper()
	if _, held := f.fleet.index.get(id); held {
		t.Fatalf("lease index still holds %q after %s returned nil", id, op)
	}
}

func repoOfLeaseFailsClosed(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	aborted, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if err := f.fleet.Abort(t.Context(), aborted); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	requireDropped(t, f, aborted.ID, "Abort")
	requireUnknown(t, f, aborted.ID, "after Abort")

	direct, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, err := f.reserver.Release(t.Context(), direct.ReservationID); err != nil {
		t.Fatalf("Reserver.Release: %v", err)
	}
	requireUnknown(t, f, direct.ID, "reservation retired through the Reserver, not the Fleet")

	requireRolledBackUnknown(t, f)

	moved, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	f.exec(`UPDATE jobs_reservation SET repo_id = 'repo-other' WHERE id = '` + moved.ReservationID + `'`)
	requireUnknown(t, f, moved.ID, "reservation row names another repo than the lease")

	live, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	f.exec(`DROP TABLE jobs_reservation`)
	repo, ok, err := f.fleet.RepoOfLease(t.Context(), live.ID)
	if repo != "" || ok || err == nil {
		t.Fatalf("RepoOfLease with the reservation table dropped = (%q, %v, %v), want (\"\", false, a read error)", repo, ok, err)
	}
	if hasSentinel(err, ErrLeaseUnknown) || strings.Contains(err.Error(), ErrLeaseUnknown.Msg) {
		t.Fatalf("err = %v, a read failure must not read as ErrLeaseUnknown", err)
	}
}

func TestFleetRepoOfLeaseSurvivesParkAndExpire(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, err := f.reserver.Park(t.Context(), l.ReservationID); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if _, err := f.fleet.ExpireStale(t.Context()); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	requireUnknown(t, f, l.ID, "a parked lease is not live")
	if _, err := f.reserver.Unpark(t.Context(), l.ReservationID); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	if repo, ok, err := f.fleet.RepoOfLease(t.Context(), l.ID); repo != "repo-a" || !ok || err != nil {
		t.Fatalf("RepoOfLease after Unpark = (%q, %v, %v), want (repo-a, true, nil): the park and the sweep must not forget the lease", repo, ok, err)
	}
}

// requireRolledBackUnknown rolls a lease's reservation back through the
// Reserver, leaving the Fleet's index intact, and expects RepoOfLease to fail
// closed.
func requireRolledBackUnknown(t *testing.T, f *fx) {
	t.Helper()
	rolled, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	failing := func(context.Context, *sql.Tx, economics.Reservation) error { return errors.New("forced rollback") }
	if _, err := f.reserver.Bind(t.Context(), rolled.ReservationID, abortPlacement(rolled), failing); err == nil {
		t.Fatal("Bind with a failing callback returned nil, want the rollback error")
	}
	if r, _, err := f.fleet.ReservationByExecution(t.Context(), rolled.ExecutionID); err != nil || r.State != economics.ReservationRolledBack {
		t.Fatalf("reservation = (%s, %v), want rolled_back", r.State, err)
	}
	requireUnknown(t, f, rolled.ID, "reservation rolled back through the Reserver with the index intact")
}
