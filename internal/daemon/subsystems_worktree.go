package daemon

// Purpose: AC/S-59.T3's HOW step 3 companion to subsystems.go's
//
//	RegisterWorktreeSweep -- RegisterWorktreeManager wires the acquired
//	=> Create / released|expired => Remove lease-lifecycle event
//	consumption. Split to its own file purely to keep subsystems.go
//	under the 300-line cap (Art.10.3), same rationale internal/jobs'
//	own worktree_mutex.go/worktree_store.go/worktree_events.go record.
//	FILES_SCOPE NOTE: this filename is not in the ticket's files_scope
//	add list; see the ticket's journal.
//
// Inputs: a real *events.Bus, a constructed *jobs.WorktreeManager, a
//
//	jobs.RepoRootResolver, and a cursor name for the bus subscription.
//
// Outputs: a live background goroutine (wt.Run) consuming
//
//	jobs.LeaseEventNamespace until ctx is canceled; Manifest records
//	Started/Failed exactly as every other Register* call here does.
//
// Constraints: no CLI verb, no RPC method (S-60.T1's scope) -- this is a
//
//	plain composition-root wiring call, matching this ticket's boundary
//	list exactly.
//
// SPORT: internal/daemon (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
)

// worktreeManagerSubsystem is the fail-loud Manifest name
// RegisterWorktreeManager reports under (R-14.87).
const worktreeManagerSubsystem = "jobs.worktree.manager"

// RegisterWorktreeManager subscribes wt to bus's lease-lifecycle
// namespace under cursorName and starts wt.Run as its own goroutine,
// bounded by ctx's cancellation (there is no separate Stop call: canceling
// ctx is what stops the goroutine). A Subscribe failure (e.g. cursorName
// already active) is recorded as Failed and returned; the goroutine's own
// terminal error, if any, is recorded as Failed asynchronously since Run
// only returns after ctx ends or the subscription's channel closes.
//
// A nil resolveRoot defaults to jobs.IdentityRepoRootResolver -- the only
// honest RepoRootResolver this tree can supply today (see
// worktree_events.go's CONTRACT GAP note); a real repo-id registry, when
// one exists, is wired here by passing it explicitly instead.
//
// CONTRACT DEVIATION: see subsystems.go's RegisterWorktreeSweep doc --
// same posture, no daemon startup path calls this yet.
func (m *Manifest) RegisterWorktreeManager(ctx context.Context, bus *events.Bus, wt *jobs.WorktreeManager, resolveRoot jobs.RepoRootResolver, cursorName string) error {
	m.Register(worktreeManagerSubsystem)
	if resolveRoot == nil {
		resolveRoot = jobs.IdentityRepoRootResolver
	}
	sub, err := bus.Subscribe(ctx, jobs.LeaseEventNamespace, cursorName, 64)
	if err != nil {
		m.Failed(worktreeManagerSubsystem, err.Error())
		return err
	}
	go func() {
		if runErr := wt.Run(ctx, sub, resolveRoot); runErr != nil {
			m.Failed(worktreeManagerSubsystem, runErr.Error())
		}
	}()
	m.Started(worktreeManagerSubsystem, fmt.Sprintf("subscribed cursor %q on namespace %q", cursorName, jobs.LeaseEventNamespace))
	return nil
}
