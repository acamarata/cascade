package jobs

// Purpose: HOW step 3's lifecycle wiring — consuming the S-59.T2 lease
//
//	lifecycle events (lease_events.go, same package: EventLeaseAcquired/
//	Released/Expired and the unexported leasePayload wire shape are read
//	directly, no cross-package seam needed) and dispatching acquired to
//	Create, released/expired to Remove — plus the worktree lifecycle's
//	own journal entries (HOW step 7). FILES_SCOPE NOTE (recorded, not
//	papered over): this filename is not in the ticket's files_scope add
//	list; split out of worktree.go purely for the 300-line cap, same
//	rationale as worktree_mutex.go/worktree_store.go.
//
// CONTRACT GAP (recorded, not guessed past): Create needs a repo ROOT
// path; a lease event's leasePayload carries only RepoID. No repo-id ->
// filesystem-path registry exists anywhere in this tree (a repo-wide grep
// for one comes up empty, matching this exact ticket's earlier
// files_scope note on the config-registration gap). RepoRootResolver is
// the minimal seam this ticket defines to cross that gap without editing
// any other ticket's file: IdentityRepoRootResolver (RepoID IS already the
// filesystem root) is the only implementation this tree can honestly
// supply today, wired at the daemon composition root
// (internal/daemon/subsystems.go). A real repo-id registry, when one
// exists, swaps this one function without touching Create/Remove/Run.
//
// Inputs: a real events.Bus Subscription over leaseEventNamespace
//
//	("jobs.lease", lease_events.go's own constant) and a RepoRootResolver.
//
// Outputs: Create/Remove side effects per event kind; contended/renewed
//
//	events are no-ops here (they carry no worktree action per this
//	ticket's full_desc HOW step 3).
//
// Constraints: no CLI verb, no RPC method (S-60.T1's scope, per this
//
//	ticket's boundary list) — Run is a plain library loop the daemon
//	composition root starts a goroutine over.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LeaseEventNamespace re-exports lease_events.go's unexported
// leaseEventNamespace so the daemon composition root (which cannot import
// an unexported identifier) can Subscribe to the exact same bus namespace
// this file's apply/Run consume, with no risk of the two literals drifting
// apart.
const LeaseEventNamespace = leaseEventNamespace

// RepoRootResolver maps a lease's RepoID to the filesystem path Create
// should check the worktree out under. See this file's CONTRACT GAP note.
type RepoRootResolver func(repoID string) (string, error)

// IdentityRepoRootResolver is the only honest production implementation
// available in this tree today: it treats RepoID as already being the
// repo's filesystem root.
func IdentityRepoRootResolver(repoID string) (string, error) {
	if repoID == "" {
		return "", cascade.New(cascade.KindInvalidInput, "jobs: empty repo id cannot resolve to a worktree root")
	}
	return repoID, nil
}

// apply dispatches one lease lifecycle event to Create/Remove.
// acquired -> Create; released/expired -> Remove; every other kind
// (contended, renewed) is a no-op (no worktree action, per HOW step 3).
func (m *WorktreeManager) apply(ctx context.Context, ev events.Event, resolveRoot RepoRootResolver) error {
	var p leasePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: decode lease event payload")
	}
	lease := ResourceLease{RepoID: p.RepoID, ScopeGlob: p.ScopeGlob, Holder: p.Holder, Epoch: p.Epoch, State: LeaseState(p.State)}

	// This namespace (leaseEventNamespace, "jobs.lease") carries ONLY
	// lease_events.go's own five EventKind values -- events.EventKind is a
	// deliberately open, cross-package taxonomy (see internal/events'
	// own doc comment), so this switch is intentionally non-exhaustive
	// over that type's full, tree-wide vocabulary; it is exhaustive over
	// the closed set THIS namespace ever publishes.
	//nolint:exhaustive // see comment above: exhaustive over this namespace's own five kinds, not events.EventKind's tree-wide vocabulary
	switch ev.Kind {
	case EventLeaseAcquired:
		root, err := resolveRoot(lease.RepoID)
		if err != nil {
			return err
		}
		_, err = m.Create(ctx, lease, root)
		return err
	case EventLeaseReleased, EventLeaseExpired:
		return m.Remove(ctx, lease)
	default: // EventLeaseContended, EventLeaseRenewed, and anything else: no worktree action
		return nil
	}
}

// Run drives sub, applying every delivered lease event until ctx is
// canceled or sub's Events channel closes. This is the production loop
// the daemon composition root (internal/daemon/subsystems.go) starts as
// its own goroutine; tests drive apply directly for determinism, or run
// Run with a bounded ctx and assert on the resulting store/git state.
func (m *WorktreeManager) Run(ctx context.Context, sub *events.Subscription, resolveRoot RepoRootResolver) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-sub.Errs:
			if ok && err != nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if err := m.apply(ctx, ev, resolveRoot); err != nil {
				return err
			}
		}
	}
}

// worktreeJournalPayload is the {lease, worktree} structured payload every
// created/removed/swept journal entry carries.
type worktreeJournalPayload struct {
	RepoID    string `json:"repo_id"`
	ScopeGlob string `json:"scope_glob"`
	Holder    string `json:"holder"`
	Path      string `json:"path"`
	Branch    string `json:"branch"`
}

// appendWorktreeJournal appends one lifecycle entry to lease's M/S-27.T1
// entity journal, under the SAME entity id lease_events.go's own
// acquired/released entries use (leaseEntityID) — a reader replaying one
// entity id sees the lease AND its worktree's full lifecycle together. A
// nil journal disables this (unit tests that only assert store/git state).
func (m *WorktreeManager) appendWorktreeJournal(ctx context.Context, lease ResourceLease, kind journal.Kind, op string, w Worktree) error {
	if m.journal == nil {
		return nil
	}
	raw, err := json.Marshal(worktreeJournalPayload{
		RepoID: lease.RepoID, ScopeGlob: lease.ScopeGlob, Holder: lease.Holder, Path: w.Path, Branch: w.Branch,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode worktree journal payload")
	}
	_, err = m.journal.Append(ctx, leaseEntityID(lease.RepoID, lease.ScopeGlob), kind, op, json.RawMessage(raw))
	return err
}
