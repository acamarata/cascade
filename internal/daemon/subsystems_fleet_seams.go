package daemon

// Purpose: the production seams of the fleet runtime's one Reserver
//
//	(subsystems_fleet_reservation.go builds it): leases through the one
//	LeaseManager, worktrees through the fenced WorktreeManager, quota from
//	the topology store on the runtime handle, attention on the daemon queue.
//
// Inputs: a built fleetReservation. Outputs: the economics.ReserverSeams
//
//	methods, and otherDaemonLive (the start sweep's OtherDaemonLive check).
//
// Constraints: a lease handle is "<repo>:<glob>@<epoch>": the jobs lease id
//
//	plus the epoch the grant was made at. Renew, release and worktree creation
//	present that epoch, so a later holder's grant of the same scope is never
//	renewed, released or bound by this reservation. Worktree removal acts
//	only on the current holder's own path (WorktreeManager.Remove).
//
// SPORT: internal/daemon (ADD, P1-CAP-10).

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// leaseHandle is a reservation's id for one lease grant.
func leaseHandle(l jobs.ResourceLease) string {
	return jobs.LeaseID(l) + "@" + strconv.FormatInt(l.Epoch, 10)
}

// lookupLease resolves a handle to the stored row and the handle's epoch.
func (f *fleetReservation) lookupLease(ctx context.Context, handle string) (jobs.ResourceLease, int64, bool, error) {
	i := strings.LastIndex(handle, "@")
	if i < 0 {
		return jobs.ResourceLease{}, 0, false, cascade.Newf(cascade.KindInvalidInput, "fleet runtime: lease handle %q has no epoch", handle)
	}
	epoch, err := strconv.ParseInt(handle[i+1:], 10, 64)
	if err != nil {
		return jobs.ResourceLease{}, 0, false, cascade.Newf(cascade.KindInvalidInput, "fleet runtime: malformed lease handle %q", handle)
	}
	l, ok, err := f.jobs.GetLeaseByID(ctx, handle[:i])
	return l, epoch, ok, err
}

// acquireLeases acquires one lease per glob for jobID, releasing what it
// took in reverse when a glob is contended or fails.
func (f *fleetReservation) acquireLeases(ctx context.Context, repoID, jobID string, globs []string) ([]string, error) {
	var got []jobs.ResourceLease
	for _, g := range globs {
		res, err := f.leases.Acquire(ctx, repoID, g, jobID)
		if res.Granted {
			got = append(got, res.Lease)
		}
		if err == nil && !res.Granted {
			err = cascade.Newf(cascade.KindConflict, "fleet runtime: lease %s:%s is held by %s", repoID, g, res.Contending.Holder)
		}
		if err != nil {
			for i := len(got) - 1; i >= 0; i-- {
				err = errors.Join(err, f.leases.Release(ctx, got[i].RepoID, got[i].ScopeGlob, got[i].Epoch))
			}
			return nil, err
		}
	}
	ids := make([]string, 0, len(got))
	for _, l := range got {
		ids = append(ids, leaseHandle(l))
	}
	return ids, nil
}

// releaseLeases releases each handle's grant in reverse; a grant that is
// gone, released or superseded by a later epoch is not this reservation's.
func (f *fleetReservation) releaseLeases(ctx context.Context, ids []string) error {
	var errs []error
	for i := len(ids) - 1; i >= 0; i-- {
		l, epoch, ok, err := f.lookupLease(ctx, ids[i])
		if err == nil && ok && l.State != jobs.LeaseReleased && l.Epoch == epoch {
			err = f.leases.Release(ctx, l.RepoID, l.ScopeGlob, epoch)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// renewLeases renews each handle's grant; a missing, released or
// superseded grant is a lost lease (KindConflict).
func (f *fleetReservation) renewLeases(ctx context.Context, ids []string) error {
	for _, id := range ids {
		l, epoch, ok, err := f.lookupLease(ctx, id)
		if err != nil {
			return err
		}
		if !ok || l.State == jobs.LeaseReleased || l.Epoch != epoch {
			return cascade.Wrapf(cascade.KindConflict, jobs.ErrLeaseFenced, "fleet runtime: lease %s lost", id)
		}
		if _, err := f.leases.Renew(ctx, l.RepoID, l.ScopeGlob, epoch); err != nil {
			return err
		}
	}
	return nil
}

// validateLeases keeps the handles jobID still holds, unexpired, at their epoch.
func (f *fleetReservation) validateLeases(ctx context.Context, jobID string, ids []string) ([]string, error) {
	now := f.clock.Now().Unix()
	var valid []string
	for _, id := range ids {
		l, epoch, ok, err := f.lookupLease(ctx, id)
		if err != nil {
			return nil, err
		}
		held := l.State == jobs.LeaseHeld || l.State == jobs.LeaseRenewing
		if ok && held && l.Holder == jobID && l.Epoch == epoch && now <= l.IssuedAt+l.TTLSeconds {
			valid = append(valid, id)
		}
	}
	return valid, nil
}

// findLeases returns the handles of the live leases jobID holds in repoID on
// any of globs, in glob order.
func (f *fleetReservation) findLeases(ctx context.Context, repoID, jobID string, globs []string) ([]string, error) {
	held, err := f.jobs.LeasesForHolder(ctx, jobID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range globs {
		scope, err := jobs.NormalizeScope(g)
		if err != nil {
			return nil, err
		}
		for _, l := range held {
			if l.RepoID == repoID && l.ScopeGlob == scope.String() && l.State.Contending() {
				ids = append(ids, leaseHandle(l))
			}
		}
	}
	return ids, nil
}

// findWorktree reports the worktree bound to a handle's (repo, scope).
func (f *fleetReservation) findWorktree(ctx context.Context, id string) (string, bool, error) {
	l, epoch, ok, err := f.lookupLease(ctx, id)
	if err != nil || !ok || l.Epoch != epoch {
		return "", false, err // gone, or superseded by a later grant
	}
	w, found, err := f.jobs.WorktreeForLease(ctx, l.RepoID, l.ScopeGlob)
	return w.Path, found, err
}

// allocateWorktree creates the worktree of a handle's grant, fenced at the
// handle's epoch; its path is the worktree id.
func (f *fleetReservation) allocateWorktree(ctx context.Context, id string) (string, error) {
	l, epoch, ok, err := f.lookupLease(ctx, id)
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "fleet runtime: lease %s not found", id)
	}
	if err != nil {
		return "", err
	}
	l.Epoch = epoch
	root, err := jobs.IdentityRepoRootResolver(l.RepoID)
	if err != nil {
		return "", err
	}
	w, err := f.worktrees.Create(ctx, l, root)
	return w.Path, err
}

// removeWorktree removes the worktree at path under its lease; Remove acts
// only on the lease holder's own path.
func (f *fleetReservation) removeWorktree(ctx context.Context, path string) error {
	w, ok, err := f.jobs.GetWorktree(ctx, path)
	if err != nil || !ok {
		return err
	}
	l, ok, err := f.jobs.GetLease(ctx, w.LeaseRepoID, w.LeaseScopeGlob)
	if err != nil || !ok {
		return err
	}
	return f.worktrees.Remove(ctx, l)
}

// domainAccount reads a quota domain and its account from the topology store.
func (f *fleetReservation) domainAccount(ctx context.Context, domainID string) (topology.QuotaDomain, topology.Account, error) {
	d, err := f.topo.GetQuotaDomain(ctx, topology.DomainID(domainID))
	if err != nil {
		return d, topology.Account{}, err
	}
	a, err := f.topo.GetAccount(ctx, d.AccountRef)
	return d, a, err
}

// buckets returns the domain's kind, its limit scope and its stored buckets.
func (f *fleetReservation) buckets(ctx context.Context, domainID string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
	d, a, err := f.domainAccount(ctx, domainID)
	if err != nil {
		return "", "", nil, err
	}
	list, err := f.quota.ListBuckets(ctx, d.ID)
	if err != nil {
		return "", "", nil, err
	}
	out := make(map[string]topology.Bucket, len(list))
	for _, b := range list {
		out[b.Name] = b
	}
	return d.Kind, topology.ResolveScope(a, d), out, nil
}

// reserveFraction is the domain account's role-default weekly reserve.
func (f *fleetReservation) reserveFraction(ctx context.Context, domainID string) (float64, error) {
	_, a, err := f.domainAccount(ctx, domainID)
	if err != nil {
		return 0, err
	}
	return economics.RoleDefaults(a.Role).PreserveWeeklyReserve, nil
}

// raiseAttention files one item per (reservation, reason) on the daemon's
// attention store.
func (f *fleetReservation) raiseAttention(ctx context.Context, id, reason string) error {
	_, err := f.attention.Push(ctx, supervision.AttentionItem{
		Kind: supervision.KindStall, SourceRef: "reservation:" + id + ":" + reason,
		ScopeRef: scope.Ref{Kind: scope.ScopeKindGeneral, ID: id},
	})
	return err
}

// otherDaemonLive reports whether the pidfile names a running daemon other
// than self. A corrupt pidfile is an error, never "not live".
func otherDaemonLive(pidPath string, prober ProcessProber, self int) (bool, error) {
	rec, ok, err := readPIDFile(pidPath)
	if err != nil {
		return false, err
	}
	if ok && rec.PID == self {
		return false, nil
	}
	return classifyPID(rec, ok, prober) == livenessRunning, nil
}

// jobsDBQuery is the runtime handle's DSN: IMMEDIATE transactions waiting under
// busy_timeout, WAL like the providers/sqlite driver on the same file, and
// synchronous=NORMAL. A lease transaction holds the write lock from BEGIN to
// COMMIT; with a full sync that is several fsyncs, so back-to-back transactions
// kept the lock nearly always held and SQLite's polling busy handler starved a
// second connection past busy_timeout once fsync is slow (SQLITE_BUSY on
// windows; WAL alone did not cure it, NORMAL did). In WAL, NORMAL appends the
// commit without an fsync (the checkpoint syncs). DURABILITY: a process crash
// loses nothing and the file is never corrupted, but an OS crash or power cut
// can lose the last commits on this handle (leases, reservations, jobs and
// outbox rows); the start sweep only settles rows that survived and does not
// recover lost ones. This is a deliberate exception to the driver's pinned
// synchronous=FULL on the same file (providers/sqlite/README.md), recorded in
// the P1-CI-22 evidence for a ruling.
const jobsDBQuery = "_busy_timeout=5000&_txlock=immediate&_journal_mode=WAL&_synchronous=NORMAL"
