// Purpose: the one place `cascade sync`'s verbs reach the sync surface,
//   so every verb asks the same object the same way.
// WHY A WRAPPER AND NOT FOUR DIRECT CALLS: the CLI is specified as a thin
//   mirror of the sync.* RPC methods, and the way a mirror stops being one
//   is a verb that reaches past it. Routing all four through one type
//   keeps the CLI's answers and the RPC's answers the same answers.
// SPORT: cli.sync surface (ADD) — P1-E17-W4-S38-T3.

package main

import (
	"context"

	syncpkg "github.com/acamarata/cascade/internal/sync"
)

// syncSurface is the CLI's view of the sync.* methods.
type syncSurface struct{ deps syncpkg.Deps }

// withSurface opens the surface one verb call uses, runs fn against it,
// and CLOSES whatever it opened.
//
// The close is the point. Opening the engine per call is right — the
// command tree is described on every invocation and must touch nothing —
// but the store it opens holds a file handle, and nothing was giving it
// back. A one-shot CLI process gets away with that because exiting closes
// it; a test harness does not, and neither does any longer-lived host.
// The W-4 hardening gate found it on the Windows lane, where an unclosed
// sqlite file cannot be deleted at all and three tests failed cleaning up
// after themselves (R-14.277).
//
// A store supplied directly (Engine set, as a test does) is NOT closed
// here: this function closes what it opened, and closing a collaborator
// somebody else owns is how a shared handle disappears underneath them.
func (d syncDeps) withSurface(fn func(syncSurface) error) error {
	engine := d.Engine
	if engine == nil && d.OpenEngine != nil {
		opened, closer := d.OpenEngine()
		engine = opened
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
	}
	return fn(syncSurface{deps: syncpkg.Deps{
		Engine: engine, PeerTier: d.PeerTier, Gate: d.Gate, Run: d.Run, Config: d.Config,
	}})
}

// Status answers sync.status.
func (s syncSurface) Status(ctx context.Context) (syncpkg.StatusResult, error) {
	return s.deps.Status(ctx)
}

// Run answers sync.run for one domain, or for every permitted one.
func (s syncSurface) Run(ctx context.Context, domain string) (syncpkg.RunResult, error) {
	return s.deps.RunDomains(ctx, domain)
}

// Conflicts answers sync.conflicts_list.
func (s syncSurface) Conflicts(context.Context) (syncpkg.ConflictsResult, error) {
	return s.deps.ConflictsList()
}

// Resolve answers sync.conflicts_resolve.
func (s syncSurface) Resolve(ctx context.Context, recordID, keep string) (syncpkg.ResolveResult, error) {
	return s.deps.Resolve(ctx, recordID, keep)
}
