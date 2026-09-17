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

// surface builds the surface one verb call uses, opening the engine at
// that moment rather than when the command tree was described.
func (d syncDeps) surface() syncSurface {
	engine := d.Engine
	if engine == nil && d.OpenEngine != nil {
		engine = d.OpenEngine()
	}
	return syncSurface{deps: syncpkg.Deps{
		Engine: engine, PeerTier: d.PeerTier, Gate: d.Gate, Run: d.Run,
	}}
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
