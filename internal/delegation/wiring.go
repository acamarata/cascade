// Purpose (this file): the seams a daemon composition root binds from outside the package:
// the Fleet lease seam, the supervised result collector Delegate starts for every
// committed spawn, and Wait, the origin-checked read of a delegation's addressed result.

package delegation

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/envelope"
)

// Leaser is the Fleet lease seam; *capacity.Fleet satisfies it.
type Leaser interface {
	Lease(ctx context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, attempt int) (capacity.Lease, error)
	Commit(ctx context.Context, l capacity.Lease) error
	Abort(ctx context.Context, l capacity.Lease) error
	Release(ctx context.Context, l capacity.Lease) error
}

// Supervisor runs a named goroutine under the daemon's manifest; *daemon.Manifest satisfies it.
type Supervisor interface {
	GoSupervised(ctx context.Context, name, detail string, fn func(context.Context) error)
}

// WaitResult is a delegation's addressed result, or its stored state at the wait timeout.
type WaitResult = waitResult

// Collection binds the result collector Delegate starts for every committed spawn.
// Ctx is the daemon run context (a collector outlives the RPC that started it);
// Ticks returns a fresh ticker for one collector or one Wait and is stopped after it.
type Collection struct {
	Ctx          context.Context
	Supervisor   Supervisor
	Notify       *notify.Service
	Ticks        func() runtime.Ticker
	MaxWallClock time.Duration
}

// WithFleet returns d with f bound as the lease seam; a nil f leaves d unchanged.
func (d Deps) WithFleet(f Leaser) Deps {
	if f != nil {
		d.fleet = f
	}
	return d
}

// BindCollection makes every later committed Delegate start a supervised collector
// and enables Wait. Every field is required and MaxWallClock must be positive.
func (s *Service) BindCollection(c Collection) error {
	if c.Ctx == nil || c.Supervisor == nil || c.Notify == nil || c.Notify.Notifier == nil || c.Ticks == nil || c.MaxWallClock <= 0 {
		return invalidf("BindCollection: a context, supervisor, notify service, ticker and positive max wall clock are required")
	}
	s.collection = &c
	return nil
}

// Wait returns the addressed result of delegation id once it arrives, or the stored
// state when timeout elapses on the injected clock. Only the delegation's origin may wait.
func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (WaitResult, error) {
	if s.collection == nil {
		return WaitResult{}, invalidf("Wait: no result collection is bound")
	}
	ticker := s.collection.Ticks()
	defer ticker.Stop()
	return s.wait(ctx, s.collection.Notify, id, timeout, ticker.C())
}

// started commits the spawned lease, then starts the collector when a collection is
// bound. A refused Commit or an unreadable record releases what the spawn holds.
func (s *Service) started(ctx context.Context, req *Request, rec envelope.Signed, lease capacity.Lease, h agents.SpawnHandle) error {
	if err := s.deps.fleet.Commit(ctx, lease); err != nil {
		return s.unwind(ctx, req, rec, h, nil, err)
	}
	if s.collection == nil {
		return nil
	}
	stored, err := s.deps.Store.GetDelegation(ctx, rec.Env.ID)
	if err != nil {
		return s.unwind(ctx, req, rec, h, func(c context.Context) error { return s.deps.fleet.Release(c, lease) }, err)
	}
	ticker := s.collection.Ticks()
	scope := &tickerScope{sup: s.collection.Supervisor, stop: ticker.Stop}
	scope.left.Store(2) // startSupervised runs the deadline watcher and the collector
	c := &collector{service: s, notify: s.collection.Notify, record: stored, env: rec.Env, handle: h,
		lease: lease, worktree: req.Worktree, start: s.deps.Clock.Now(), maxWallClock: s.collection.MaxWallClock, ticks: ticker.C()}
	c.startSupervised(s.collection.Ctx, scope)
	return nil
}

// unwind releases the handle, the worktree, then the lease (when committed), revokes the
// child, fails the record and returns cause; cleanup errors are secondary to cause.
func (s *Service) unwind(ctx context.Context, req *Request, rec envelope.Signed, h agents.SpawnHandle, release func(context.Context) error, cause error) error {
	cleanup := context.WithoutCancel(ctx)
	if h.Release != nil {
		_ = h.Release(cleanup)
	}
	_ = req.Worktree.Release()
	if release != nil {
		_ = release(cleanup)
	}
	s.deps.Minter.Revoke(rec.Env.Principal)
	_ = s.deps.Store.FinishDelegation(cleanup, rec.Env.ID, jobs.DelegationFailed, "spawn not started: "+cause.Error())
	return cause
}

// tickerScope stops a collector's ticker once both of its supervised goroutines return.
type tickerScope struct {
	sup  Supervisor
	stop func()
	left atomic.Int32
}

// GoSupervised runs fn under the wrapped supervisor and counts its return.
func (t *tickerScope) GoSupervised(ctx context.Context, name, detail string, fn func(context.Context) error) {
	t.sup.GoSupervised(ctx, name, detail, func(ctx context.Context) error {
		defer func() {
			if t.left.Add(-1) == 0 {
				t.stop()
			}
		}()
		return fn(ctx)
	})
}
