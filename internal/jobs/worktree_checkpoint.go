package jobs

// Purpose: the lease checkpoint producer (P1-CI-10, contract:ci-daemon-
// composition). CheckpointWatcher polls the HEAD of every held lease's
// worktree on an injected ticker and publishes one EventLeaseCheckpoint on
// the jobs.lease namespace per HEAD sha it has not yet reported for that
// lease. The consumer (the daemon's CI composition) turns each event into a
// ci.Dispatcher checkpoint; this package never imports internal/ci.
//
// The poll lives here rather than in worktree_events.go: that file's switch
// handles the five lifecycle kinds of jobs.lease and is fed by the lease
// event sink. Its default arm ignores any other kind, so a checkpoint event
// on the same namespace never moves a worktree.
//
// Semantics: the first sighting of a lease reports its current HEAD (a
// restarted daemon must not lose a commit made while it was down; the
// consumer skips a HEAD that equals the lease's base). A HEAD equal to the
// last reported sha is not reported again. A lease that is no longer held,
// or has no worktree row, is skipped and forgotten, so nothing is published
// for it after release. A git failure skips that lease for this tick when
// the worktree directory is gone or the lease, re-read, is no longer held at
// the same epoch (it is being released and removed). Otherwise the failure
// is logged and counted per lease; maxGitFailures consecutive failures of
// one still-held lease end Run with the error, so the supervised goroutine
// reports it. A store failure ends Run at once.
//
// SPORT: internal.jobs.EventLeaseCheckpoint/ADDED, internal.jobs.CheckpointWatcher/ADDED,
// internal.jobs.NewCheckpointWatcher/ADDED (P1-CI-10).

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EventLeaseCheckpoint is published on the jobs.lease namespace once per
// new HEAD commit observed in a held lease's worktree.
const EventLeaseCheckpoint events.EventKind = "lease_checkpoint"

// checkpointSource is the publisher name on every checkpoint event.
const checkpointSource = "jobs.checkpoint"

// checkpointPayload is EventLeaseCheckpoint's wire shape.
type checkpointPayload struct {
	JobID      string `json:"job_id"`
	LeaseID    string `json:"lease_id"`
	RepoID     string `json:"repo_id"`
	ScopeGlob  string `json:"scope_glob"`
	Epoch      int64  `json:"epoch"`
	CommitSHA  string `json:"commit_sha"`
	ObservedAt int64  `json:"observed_at"`
}

// CheckpointWatcher polls held leases' worktrees for new HEAD commits.
// Construct with NewCheckpointWatcher; Run is called once.
type CheckpointWatcher struct {
	store  *Store
	bus    *events.Bus
	git    func(ctx context.Context, dir string) (string, error)
	ticker runtime.Ticker
	// clock stamps observed_at. The contract signature carries no clock,
	// so the constructor installs runtime.SystemClock; tests override it.
	clock runtime.Clock

	mu    sync.Mutex
	seen  map[string]string // lease id -> last reported HEAD sha
	fails map[string]int    // lease id -> consecutive git failures
}

// maxGitFailures consecutive git failures on one still-held lease end Run.
const maxGitFailures = 3

// NewCheckpointWatcher validates its inputs. git returns the HEAD sha of
// the worktree at dir; ticker paces the poll and is stopped when Run ends.
func NewCheckpointWatcher(store *Store, bus *events.Bus, git func(ctx context.Context, dir string) (string, error),
	ticker runtime.Ticker) (*CheckpointWatcher, error) {
	missing := ""
	switch {
	case store == nil:
		missing = "store"
	case bus == nil:
		missing = "bus"
	case git == nil:
		missing = "git"
	case ticker == nil:
		missing = "ticker"
	}
	if missing != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: NewCheckpointWatcher requires a non-nil %s", missing)
	}
	return &CheckpointWatcher{store: store, bus: bus, git: git, ticker: ticker, clock: runtime.SystemClock{},
		seen: map[string]string{}, fails: map[string]int{}}, nil
}

// Run polls once per tick until ctx is cancelled or the ticker channel
// closes (both a clean stop, nil). A store or publish failure is returned.
func (w *CheckpointWatcher) Run(ctx context.Context) error {
	defer w.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.ticker.C():
			if !ok {
				return nil
			}
			if err := w.poll(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// poll walks every lease page and reports each held lease's new HEAD.
func (w *CheckpointWatcher) poll(ctx context.Context) error {
	held := map[string]bool{}
	cursor := ""
	for {
		page, next, err := w.store.ListLeases(ctx, LeaseFilter{Cursor: cursor})
		if err != nil {
			return err
		}
		for _, l := range page {
			if l.State != LeaseHeld && l.State != LeaseRenewing {
				continue
			}
			id := LeaseID(l)
			held[id] = true
			if err := w.observe(ctx, id, l); err != nil {
				return err
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	w.forget(held)
	return nil
}

// observe reports l's worktree HEAD when it differs from the last report.
func (w *CheckpointWatcher) observe(ctx context.Context, id string, l ResourceLease) error {
	wt, ok, err := w.store.WorktreeForLease(ctx, l.RepoID, l.ScopeGlob)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	sha, err := w.git(ctx, wt.Path)
	if err != nil {
		return w.gitFailed(ctx, id, l, wt.Path, err)
	}
	sha = strings.TrimSpace(sha)
	w.mu.Lock()
	delete(w.fails, id)
	last := w.seen[id]
	w.mu.Unlock()
	if sha == "" || sha == last {
		return nil
	}
	if err := w.publish(ctx, id, l, sha); err != nil {
		return err
	}
	w.mu.Lock()
	w.seen[id] = sha
	w.mu.Unlock()
	return nil
}

// gitFailed handles one git failure on l's worktree at path (file header).
func (w *CheckpointWatcher) gitFailed(ctx context.Context, id string, l ResourceLease, path string, gitErr error) error {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil // the worktree is gone: removal in progress
	}
	cur, ok, err := w.store.GetLease(ctx, l.RepoID, l.ScopeGlob)
	if err != nil {
		return err
	}
	if !ok || cur.Epoch != l.Epoch || (cur.State != LeaseHeld && cur.State != LeaseRenewing) {
		return nil // released or re-issued after this tick listed it: removal in progress
	}
	w.mu.Lock()
	w.fails[id]++
	n := w.fails[id]
	w.mu.Unlock()
	slog.Default().Warn("jobs: checkpoint watcher could not read a leased worktree's HEAD",
		"lease", id, "path", path, "consecutive", n, "limit", maxGitFailures, "error", gitErr)
	if n < maxGitFailures {
		return nil
	}
	return cascade.Wrapf(cascade.KindUnavailable, gitErr,
		"jobs: reading HEAD of leased worktree %s failed %d consecutive times", path, n)
}

// publish emits one EventLeaseCheckpoint for (l, sha).
func (w *CheckpointWatcher) publish(ctx context.Context, id string, l ResourceLease, sha string) error {
	raw, err := json.Marshal(checkpointPayload{
		JobID: l.Holder, LeaseID: id, RepoID: l.RepoID, ScopeGlob: l.ScopeGlob, Epoch: l.Epoch,
		CommitSHA: sha, ObservedAt: w.clock.Now().Unix(),
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode lease checkpoint payload")
	}
	_, err = w.bus.Publish(ctx, leaseEventNamespace, EventLeaseCheckpoint, checkpointSource, raw)
	return err
}

// forget drops the last-reported sha of every lease no longer held, so a
// later re-acquire of the same scope reports its HEAD again.
func (w *CheckpointWatcher) forget(held map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.seen {
		if !held[id] {
			delete(w.seen, id)
		}
	}
	for id := range w.fails {
		if !held[id] {
			delete(w.fails, id)
		}
	}
}
