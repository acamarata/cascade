package jobs

// Purpose: R-21.169's controller-singleton guard as applied to
//
//	Scheduler.Advance and Scheduler.Resume -- a node daemon reaching
//	either refuses with a typed ErrNotController-shaped error rather
//	than scheduling or resuming locally.
//
// CONTRACT NOTE (files_scope, quoted in the journal): the full_desc asks
// this file to take "the existing B/S-02.T2 database advisory lock
// once at subsystem start and hold it for the daemon's life", with a
// typed ErrNotController "defined with the lease authority in S-59.T2".
// The real tree already ships this exact guard, one layer up:
// internal/nodes/controller.go (P1-E36-W7-S72-T2) defines Role,
// ResolveRole, RequireController(role, method) and ErrNotController
// (a func, not this package's jobs.ErrNotController value), and its own
// doc comment says in so many words: "AC/S-59.T5's own handlers apply
// it (forward note ... that file change belongs to AC/S-59.T5, not
// here)". Re-deriving a second advisory-lock acquisition here would
// create TWO independent controller-singleton mechanisms that could
// disagree about which daemon is the controller -- strictly worse than
// reusing the one R-21.169 authority already ships. This file therefore
// WIRES nodes.RequireController/nodes.RoleFromContext as Advance's and
// Resume's guard, via requireControllerCtx, rather than building a
// second lock. Guard(ctx) is kept as the literal contract-named entry
// point, implemented in terms of the real primitive.
//
// jobs.ErrNotController (lease.go, S-59.T2) remains LeaseManager's own
// guard for Acquire/Release -- a node daemon calling those still refuses
// via that path; this file does not change lease.go (out of files_scope)
// and does not duplicate its error value, only its guarded-methods
// intent for the two NEW entry points this ticket owns.
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5).

import (
	"context"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Guard reports whether the calling daemon (as resolved by
// nodes.RoleFromContext, itself set by nodes.WithRole at connection
// time) may invoke method -- one of "job.advance", "job.resume",
// "lease.acquire" or "lease.release" (R-21.169's guarded set, extended
// here with the two entry points this ticket adds). A node daemon
// refuses with a typed KindPermissionDenied error; a controller daemon
// (or a context carrying no role at all, which nodes.RoleFromContext
// resolves to the more restrictive RoleNode -- so an un-annotated ctx
// ALSO refuses, fail-closed) is the only path through.
func Guard(ctx context.Context, method string) error {
	return requireControllerCtx(ctx, method)
}

// requireControllerCtx is Advance's and Resume's shared guard call.
func requireControllerCtx(ctx context.Context, method string) error {
	role := nodes.RoleFromContext(ctx)
	if err := nodes.RequireController(role, method); err != nil {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "jobs: scheduler: "+method+" refused on a non-controller daemon")
	}
	return nil
}
