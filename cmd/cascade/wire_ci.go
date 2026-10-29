//go:build !windows

package main

// Purpose: the "ci" daemon registration (P1-CI-10, contract:ci-daemon-
// composition). It builds the one daemon.CIRuntime, runs Dispatcher.Resume
// before the socket serves (the registration runs inside buildRPCServer,
// before daemon.Run listens) and reports the resume counts as the status.get
// "ci" row. It then starts, under Manifest.GoSupervised, the lease checkpoint
// watcher (internal/jobs) and the consumer of its EventLeaseCheckpoint
// events. The consumer builds each JobRef from the job store (job row, latest
// execution, lease epoch, worktree) and calls Dispatcher.Checkpoint. Only the
// job, lease and commit identity come from the event payload; tier, scope and
// risk come from the job row.
//
// SPORT: cmd.cascade.wireCI/ADDED (P1-CI-10).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	ciSubsystem          = "ci"
	ciWatcherSubsystem   = "ci.checkpoint-watcher"
	ciConsumerSubsystem  = "ci.checkpoint-consumer"
	ciCheckpointCursor   = "ci-checkpoint"
	ciCheckpointBuffer   = 64
	ciCheckpointInterval = 5 * time.Second
)

// newCICheckpointTicker paces the checkpoint poll; tests replace it.
var newCICheckpointTicker = func() runtime.Ticker { return runtime.NewSystemTicker(ciCheckpointInterval) }

var _ = registerDaemonWiring(daemonRegistration{Name: ciSubsystem, Phase: phaseSupervised, Order: 20, Wire: wireCI})

func wireCI(w *daemonWiring) error {
	fr, cfg := w.Deps.Fleet, w.Deps.Config
	if fr == nil || cfg == nil || w.Bus == nil {
		w.Manifest.Skipped(ciSubsystem, "no fleet runtime, config or bus")
		return nil
	}
	attention, err := newCIAttentionPusher(w.Store, w.Clock, w.Bus, cfg.CIPolicy.PrivateRepos)
	if err != nil {
		return err
	}
	rt, err := daemon.NewCISubsystem(w.Ctx, fr, cfg, w.Bus, attention, w.Clock)
	if err != nil {
		return err
	}
	w.Deps.CI = rt
	rep, err := rt.Dispatcher().Resume(w.Ctx)
	if err != nil {
		return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "ci: resuming the dispatch outbox")
	}
	w.Manifest.Started(ciSubsystem, fmt.Sprintf("resume: %d redispatched, %d confirmed, %d dropped, %d unmatched",
		rep.Redispatched, rep.Confirmed, rep.Dropped, rep.Unmatched))
	return startCICheckpoints(w, fr, rt)
}

func kindOr(err error, fallback cascade.Kind) cascade.Kind {
	if k, ok := cascade.KindOf(err); ok {
		return k
	}
	return fallback
}

// startCICheckpoints subscribes the consumer first (so no checkpoint the
// watcher publishes is missed) and then starts the watcher.
func startCICheckpoints(w *daemonWiring, fr *daemon.FleetRuntime, rt *daemon.CIRuntime) error {
	watcher, err := jobs.NewCheckpointWatcher(fr.Jobs(), w.Bus, gitHead, newCICheckpointTicker())
	if err != nil {
		return err
	}
	sub, err := w.Bus.Subscribe(w.Ctx, "jobs.lease", ciCheckpointCursor, ciCheckpointBuffer)
	if err != nil {
		return err
	}
	c := ciConsumer{fr: fr, rt: rt, w: w}
	w.Manifest.GoSupervised(w.Ctx, ciConsumerSubsystem, "lease checkpoints to the CI dispatcher", func(ctx context.Context) error {
		defer func() { _ = sub.Unsubscribe() }()
		return c.run(ctx, sub)
	})
	w.Manifest.GoSupervised(w.Ctx, ciWatcherSubsystem, "polling leased worktrees for new commits", watcher.Run)
	return nil
}

type ciConsumer struct {
	fr *daemon.FleetRuntime
	rt *daemon.CIRuntime
	w  *daemonWiring
}

// ciCheckpointEvent is the subset of jobs' lease_checkpoint payload the
// consumer reads: identity only.
type ciCheckpointEvent struct {
	JobID     string `json:"job_id"`
	RepoID    string `json:"repo_id"`
	ScopeGlob string `json:"scope_glob"`
	CommitSHA string `json:"commit_sha"`
}

func (c ciConsumer) run(ctx context.Context, sub *events.Subscription) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-sub.Errs:
			if ok && err != nil && ctx.Err() == nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if ev.Kind != jobs.EventLeaseCheckpoint {
				continue
			}
			if err := c.handle(ctx, ev); err != nil && ctx.Err() == nil {
				// One refused checkpoint (fenced, out of scope, stale) never stops the consumer.
				c.logger().Warn("ci: checkpoint not dispatched", "seq", ev.Seq, "error", err)
			}
		}
	}
}

func (c ciConsumer) logger() *slog.Logger {
	if c.w.Logger != nil {
		return c.w.Logger
	}
	return slog.Default()
}

func (c ciConsumer) handle(ctx context.Context, ev events.Event) error {
	var p ciCheckpointEvent
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "ci: decode lease checkpoint")
	}
	ref, ok, err := c.jobRef(ctx, p)
	if err != nil || !ok {
		return err
	}
	_, err = c.rt.Dispatcher().Checkpoint(ctx, ref, c.rt.Model(ref.RepoRoot))
	return err
}

// jobRef builds the checkpoint's JobRef from the store. ok is false when
// the lease is gone or no longer the job's, or the commit is the lease's
// base (nothing to check yet).
func (c ciConsumer) jobRef(ctx context.Context, p ciCheckpointEvent) (ci.JobRef, bool, error) {
	store := c.fr.Jobs()
	job, okJob, err := store.GetJob(ctx, p.JobID)
	if err != nil || !okJob {
		return ci.JobRef{}, false, err
	}
	lease, okLease, err := store.GetLease(ctx, p.RepoID, p.ScopeGlob)
	if err != nil || !okLease || lease.Holder != job.ID ||
		(lease.State != jobs.LeaseHeld && lease.State != jobs.LeaseRenewing) {
		return ci.JobRef{}, false, err
	}
	wt, okWT, err := store.WorktreeForLease(ctx, lease.RepoID, lease.ScopeGlob)
	if err != nil || !okWT {
		return ci.JobRef{}, false, err
	}
	// The repo root is the worktree row's resolved Repo (worktree.go), never the lease's repo id.
	base, err := gitOut(ctx, wt.Repo, "merge-base", p.CommitSHA, "HEAD")
	if err != nil || base == p.CommitSHA {
		return ci.JobRef{}, false, err
	}
	attemptID, generation, err := latestAttempt(ctx, c.fr.JobsDB(), store, job.ID)
	if err != nil {
		return ci.JobRef{}, false, err
	}
	prefixes, err := scopePrefixes(job.MutableScope)
	if err != nil {
		return ci.JobRef{}, false, err
	}
	changed, err := changedPaths(ctx, wt.Repo, base, p.CommitSHA)
	if err != nil {
		return ci.JobRef{}, false, err
	}
	prefixes = exactLiterals(changed, prefixes)
	return ci.JobRef{
		JobID: job.ID, RepoRoot: wt.Repo, WorktreeRoot: wt.Path, Lease: lease, LeaseEpoch: lease.Epoch,
		AttemptID: attemptID, AttemptGeneration: generation, CheckpointCommit: p.CommitSHA, BaseCommit: base,
		ScopePrefixes: prefixes, PlannedRisk: jobs.RiskClass(job.RiskClass), Sensitivity: provider.TierOrLocalOnly(job.DataClass),
	}, true, nil
}

// latestAttempt returns the job's highest-attempt execution (id and
// attempt), or ("", 0) when none is recorded yet.
func latestAttempt(ctx context.Context, db *sql.DB, store *jobs.Store, jobID string) (string, int64, error) {
	var id string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM jobs_execution WHERE job_id = ? ORDER BY attempt DESC LIMIT 1`, jobID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, cascade.Wrap(cascade.KindUnavailable, err, "ci: reading the job's latest execution")
	}
	e, ok, err := store.GetExecution(ctx, id)
	if err != nil || !ok {
		return "", 0, err
	}
	return e.ID, int64(e.Attempt), nil
}

// scopePrefixes turns the job's mutable scope (comma-separated, validated by
// jobs.NormalizeScope) into the path prefixes the dispatcher's scope check
// uses: a literal path (the planner's form) covers itself, "dir/**" covers
// "dir/", and "**" yields none, so its paths fail closed (jobs.Reclassify
// never treats "" as covering). NormalizeScope refuses other globs.
func scopePrefixes(mutable string) ([]string, error) {
	if _, err := jobs.NormalizeScope(mutable); err != nil {
		return nil, err
	}
	var out []string
	for _, part := range strings.Split(mutable, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "./")
		if !strings.ContainsAny(part, "*?[{") {
			out = append(out, path.Clean(part))
			continue
		}
		s, err := jobs.NormalizeScope(part)
		if err != nil {
			return nil, err
		}
		for _, n := range strings.Split(s.String(), ",") {
			if n != "**" && strings.HasSuffix(n, "/**") {
				out = append(out, strings.TrimSuffix(n, "**"))
			}
		}
	}
	return out, nil
}

// exactLiterals keeps literal scope entries exact: when a changed path only
// starts with a literal (src/a.go.orig for src/a.go), every literal is
// dropped and the dispatcher refuses the checkpoint (fail closed).
func exactLiterals(changed, prefixes []string) []string {
	for _, c := range changed {
		for _, p := range prefixes {
			if !strings.HasSuffix(p, "/") && c != p && strings.HasPrefix(c, p) {
				var dirs []string
				for _, q := range prefixes {
					if strings.HasSuffix(q, "/") {
						dirs = append(dirs, q)
					}
				}
				return dirs
			}
		}
	}
	return prefixes
}

// changedPaths lists the paths changed between base and commit, NUL-separated
// (-z) so a name with spaces or non-ASCII bytes is one exact path.
func changedPaths(ctx context.Context, repo, base, commit string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "diff", "-z", "--name-only", "--no-renames", base, commit).Output()
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "ci: git diff in %s", repo)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func gitHead(ctx context.Context, dir string) (string, error) {
	return gitOut(ctx, dir, "rev-parse", "HEAD")
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "ci: git %s in %s", args[0], dir)
	}
	return strings.TrimSpace(string(out)), nil
}
