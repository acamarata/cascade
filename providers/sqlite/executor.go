// Purpose: the single write-connection executor + per-domain fairness
//   queue + domain-ownership registry the driver's Put/Delete/Tx all route
//   through, so exactly one SQLite write transaction is ever in flight for
//   the whole process (06-FORGE-SPEC.md §2: "single write-connection
//   executor with per-domain fairness queue").
// Inputs: submit(ctx, domain, fn) enqueues fn as one write transaction,
//   fairness-tagged by domain; OwnDomain(domain) claims coarser-grained
//   exclusive ownership of a domain for a caller-defined span of work.
// Outputs: submit blocks until fn's transaction commits/rolls back (or ctx
//   is done, or the executor is closed) and returns fn's error unchanged;
//   OwnDomain returns a release func or ErrDomainOwned.
// Constraints: this file never calls time.Now/time.Since (forbidigo) —
//   submit's blocking wait is entirely ctx/channel-driven, no polling.
// SPORT: providers.sqlite.WriteExecutor/ADDED,
//   providers.sqlite.DomainRegistry/ADDED (P1-E02-W1-S02-T2).

package sqlite

import (
	"context"
	"database/sql"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrDomainOwned is the sentinel DomainRegistry.Acquire returns when a
// second caller attempts to own a domain another caller already holds. It
// wraps the frozen pkg/cascade taxonomy's KindConflict (R-14.3) rather than
// adding a new kind, per pkg/cascade's own package doc: domain-specific
// sentinels live in their owning package and each wraps exactly one frozen
// Kind.
var ErrDomainOwned = cascade.New(cascade.KindConflict, "sqlite: domain already owned by another writer")

// ErrDaemonOwnsStore is the §D-3 arbitration refusal Open returns when
// either the injected SocketProbe reports a live daemon, or the OS-level
// exclusive flock is already held by another process. Both cases mean the
// same thing to a caller: this store is owned elsewhere right now, and
// daemonless access is refused rather than corrupting the write-executor's
// single-writer invariant across two processes.
var ErrDaemonOwnsStore = cascade.New(cascade.KindConflict, "sqlite: store is owned by a live daemon (daemon-owns-store refusal)")

// CapOp identifies which operation a cross-domain capability check
// covers. Declared locally rather than imported from
// internal/storage.Op: providers/** may import pkg/** only, never
// internal/** (Art.10.2, depguard's plugins-providers-boundary rule) — the
// same reason SocketProbe and Migrator are locally-declared, duck-typed
// injection seams rather than direct internal/ imports. A composition-root
// adapter (outside this package) is what maps internal/storage.Op onto
// CapOp when it wires a real *storage.CapabilityRegistry in as a
// GrantChecker.
type CapOp uint8

const (
	// CapOpRead covers Get and Scan (cross-domain reads). Mirrors
	// internal/storage.OpRead's bit position so a straightforward adapter
	// need not remap values, though nothing in this package assumes that.
	CapOpRead CapOp = 1 << iota
	// CapOpWrite covers Put, Delete, and Tx (cross-domain writes).
	CapOpWrite
)

// GrantChecker is the injected cross-domain capability-grant check
// Scoped's writes (via submitScoped, below) and reads (scope.go's
// checkAccess) call before letting one domain's Store handle touch
// another domain's data. A nil GrantChecker means "this Driver was opened
// unscoped" — see Driver.Scoped's doc — and every domain-scoped store
// requires a non-nil checker: submitScoped and checkAccess both treat a
// nil checker as an automatic denial, never a silent bypass. The real
// implementation, internal/storage.CapabilityRegistry, is wired in by the
// composition root (cmd/ or internal/daemon) through a thin adapter this
// package never imports directly — the ErrScopeDenied doc explains what
// that adapter has NOT yet been wired for (Art.1: no caller assembles it
// yet outside this package's own tests).
type GrantChecker interface {
	// Check reports whether srcDomain may perform op against dstDomain's
	// data right now. A non-nil error means "denied" — submitScoped and
	// checkAccess propagate it unchanged to the caller, never
	// re-wrapping or discarding it.
	Check(ctx context.Context, srcDomain, dstDomain string, op CapOp) error
}

// ErrScopeDenied is the fail-closed sentinel a domain-scoped Store (see
// Driver.Scoped) returns when a cross-domain operation has no injected
// GrantChecker to consult at all — the "nil registry" half of this
// ticket's fail-closed contract (a failed Check call instead propagates
// whatever typed error the checker itself returned, e.g.
// internal/storage.ErrDomainForbidden through the composition-root
// adapter). Wraps cascade.KindPermissionDenied, the same Kind
// internal/storage.ErrDomainForbidden wraps, so a caller inspecting only
// the Kind sees one consistent "access denied" classification regardless
// of which layer produced it.
var ErrScopeDenied = cascade.New(cascade.KindPermissionDenied, "sqlite: cross-domain access denied (no capability checker configured)")

// submitScoped is submit's domain-scoped variant: fn reaches the write
// queue only when dstDomain equals srcDomain (no cross-domain check
// needed — a domain never needs a grant to write its own data), or
// checker.Check grants srcDomain the CapOpWrite it needs against
// dstDomain. A nil checker or a failed Check denies the write BEFORE it
// is ever queued — submit is never called on the denied path, so the
// write genuinely never executes, not merely "executes but is later
// rejected."
func (e *WriteExecutor) submitScoped(ctx context.Context, srcDomain, dstDomain string, checker GrantChecker, fn func(*sql.Tx) error) error {
	if srcDomain != dstDomain {
		if checker == nil {
			return ErrScopeDenied
		}
		if err := checker.Check(ctx, srcDomain, dstDomain, CapOpWrite); err != nil {
			return err
		}
	}
	return e.submit(ctx, dstDomain, fn)
}

// writeJob is one unit of work the executor's drainer runs as a single
// *sql.Tx against the write connection.
type writeJob struct {
	domain string
	fn     func(*sql.Tx) error
	ctx    context.Context
	result chan error
}

// WriteExecutor serializes every write in the process through one
// *sql.DB connection (capped at MaxOpenConns(1) by the caller), draining
// per-domain queues in round-robin order so a burst of writes to one
// domain cannot starve another domain's queued write indefinitely.
type WriteExecutor struct {
	db      *sql.DB
	mu      sync.Mutex
	queues  map[string][]*writeJob
	order   []string // round-robin domain order; no duplicates
	wake    chan struct{}
	closeCh chan struct{}
	done    chan struct{}
	once    sync.Once
}

// newWriteExecutor starts the drainer goroutine and returns the ready
// executor.
func newWriteExecutor(db *sql.DB) *WriteExecutor {
	e := &WriteExecutor{
		db:      db,
		queues:  make(map[string][]*writeJob),
		wake:    make(chan struct{}, 1),
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go e.run()
	return e
}

// submit enqueues fn under domain's fairness slot and blocks until it
// completes, ctx is done, or the executor is closed. fn's own error is
// returned unchanged (submit adds no wrapping), so callers see exactly the
// taxonomy error fn produced.
func (e *WriteExecutor) submit(ctx context.Context, domain string, fn func(*sql.Tx) error) error {
	j := &writeJob{domain: domain, fn: fn, ctx: ctx, result: make(chan error, 1)}
	e.mu.Lock()
	if _, exists := e.queues[domain]; !exists {
		e.order = append(e.order, domain)
	}
	e.queues[domain] = append(e.queues[domain], j)
	e.mu.Unlock()

	select {
	case e.wake <- struct{}{}:
	default:
	}

	select {
	case err := <-j.result:
		return err
	case <-ctx.Done():
		return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "sqlite: write canceled")
	case <-e.done:
		return cascade.New(cascade.KindUnavailable, "sqlite: write executor closed")
	}
}

// close stops the drainer and waits for it to exit. Idempotent.
func (e *WriteExecutor) close() {
	e.once.Do(func() { close(e.closeCh) })
	<-e.done
}

// run is the drainer goroutine: pop the next round-robin job and execute
// it, or block on new work / shutdown when every queue is empty.
func (e *WriteExecutor) run() {
	defer close(e.done)
	cursor := 0
	for {
		e.mu.Lock()
		j := e.popNext(&cursor)
		e.mu.Unlock()
		if j == nil {
			select {
			case <-e.wake:
				continue
			case <-e.closeCh:
				return
			}
		}
		e.runJob(j)
	}
}

// popNext returns the next job in round-robin domain order starting at
// *cursor, or nil if every queue is empty. Caller holds e.mu.
func (e *WriteExecutor) popNext(cursor *int) *writeJob {
	n := len(e.order)
	for i := 0; i < n; i++ {
		idx := (*cursor + i) % n
		domain := e.order[idx]
		q := e.queues[domain]
		if len(q) == 0 {
			continue
		}
		j := q[0]
		e.queues[domain] = q[1:]
		if len(e.queues[domain]) == 0 {
			delete(e.queues, domain)
			e.order = append(e.order[:idx], e.order[idx+1:]...)
			*cursor = idx
		} else {
			*cursor = idx + 1
		}
		return j
	}
	return nil
}

// runJob executes j.fn as one *sql.Tx against the write connection and
// delivers the result. j.result is buffered(1), so this never blocks even
// if submit's caller already gave up on ctx.
func (e *WriteExecutor) runJob(j *writeJob) {
	tx, err := e.db.BeginTx(j.ctx, nil)
	if err != nil {
		j.result <- cascade.Wrap(cascade.KindUnavailable, err, "sqlite: begin write tx")
		return
	}
	if err := j.fn(tx); err != nil {
		_ = tx.Rollback()
		j.result <- err
		return
	}
	if err := tx.Commit(); err != nil {
		j.result <- cascade.Wrap(cascade.KindUnavailable, err, "sqlite: commit write tx")
		return
	}
	j.result <- nil
}

// DomainRegistry is a coarser-grained exclusivity lock than the write
// executor's per-op fairness queue: it lets a subsystem claim exclusive
// ownership of a whole domain across a multi-step span of work (e.g. a
// read-modify-write sequence spanning several Store calls), which the
// executor's per-op serialization alone does not prevent two callers from
// interleaving.
//
// DESIGN DECISION (contract text says "goroutine-keyed map"): Go exposes
// no stable goroutine-ID API, so ownership is tracked by an explicit
// acquire/release token (the func Acquire returns) rather than a literal
// goroutine ID — the same exclusivity guarantee, expressed the idiomatic
// Go way. See providers/sqlite/README.md "Domain registry" for the full
// rationale.
type DomainRegistry struct {
	mu     sync.Mutex
	owners map[string]struct{}
}

// newDomainRegistry returns an empty DomainRegistry.
func newDomainRegistry() *DomainRegistry {
	return &DomainRegistry{owners: make(map[string]struct{})}
}

// Acquire claims exclusive ownership of domain. A second Acquire of the
// same domain before the first is released returns ErrDomainOwned.
func (r *DomainRegistry) Acquire(domain string) (release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, held := r.owners[domain]; held {
		return nil, ErrDomainOwned
	}
	r.owners[domain] = struct{}{}
	return func() {
		r.mu.Lock()
		delete(r.owners, domain)
		r.mu.Unlock()
	}, nil
}

// OwnDomain claims exclusive ownership of domain on d's registry. See
// DomainRegistry.Acquire.
func (d *Driver) OwnDomain(domain string) (release func(), err error) {
	return d.registry.Acquire(domain)
}
