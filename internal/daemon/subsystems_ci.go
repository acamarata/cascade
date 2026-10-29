package daemon

// Purpose: the daemon's one streaming CI dispatcher (P1-CI-10,
// contract:ci-daemon-composition). NewCISubsystem builds the only
// ci.Dispatcher a daemon holds, over FleetRuntime's one cascade.db handle
// (the ci_results schema is applied to it, no second handle is opened), the
// LeaseManager's Fence and the WorktreeManager's Snapshot. The requirement
// model's Config comes from ci.ConfigFromRuntime only.
//
// The executor is the local clean-checkout executor built from [ci.local]:
// lint and format run the lint list, compile runs the build list, unit runs
// the test list. A requirement kind with no configured command (integration,
// architecture and security always; any kind whose [ci.local] list is empty)
// is never reported as passed and never left pending: it gets an explicit
// terminal ci_run with status completed, conclusion skipped and one ci_job
// whose name carries the reason, executor kind "local-not-configured",
// Passed=false. Its outbox row is confirmed like any terminal, so a restart
// does not retry it. P1-CI-03 replaces this through SetExecutor; from then
// every kind goes to the installed executor.
//
// Every terminal sub-job is checked against its ci_run_stream marker
// (ci.RunViaStream). A terminal whose run is not marked as a stream run is
// an integrity fault: it raises one attention item.
//
// SPORT: internal.daemon.CIRuntime/ADDED, internal.daemon.NewCISubsystem/ADDED (P1-CI-10).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CIRuntime owns the daemon's one ci.Dispatcher. Build it with
// NewCISubsystem; it is safe for concurrent use.
type CIRuntime struct {
	ctx       context.Context
	db        *sql.DB
	cfg       ci.Config
	disp      *ci.Dispatcher
	attention ci.AttentionPusher
	clock     runtime.Clock

	mu sync.Mutex
	// exec runs every kind in configured (nil when [ci.local] has no
	// command). configured is nil once SetExecutor installed an executor.
	exec       ci.SubJobExecutor
	configured map[ci.RequirementKind]bool
	started    bool
}

// executorKindNotConfigured is the executor kind of a not-configured terminal.
const executorKindNotConfigured = "local-not-configured"

// NewCISubsystem builds the CI runtime over fr. ctx bounds the terminal
// marker checks; cfg is the loaded daemon config.
func NewCISubsystem(ctx context.Context, fr *FleetRuntime, cfg *runtime.Config, bus *events.Bus,
	attention ci.AttentionPusher, clock runtime.Clock) (*CIRuntime, error) {
	if err := ciInputsPresent(fr, cfg, bus, attention, clock); err != nil {
		return nil, err
	}
	db := fr.JobsDB()
	if err := ci.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return nil, err
	}
	local, configured, err := newLocalCIExecutor(fr.res.paths, cfg, db, bus, clock)
	if err != nil {
		return nil, err
	}
	r := &CIRuntime{ctx: ctx, db: db, cfg: ci.ConfigFromRuntime(*cfg), attention: attention, clock: clock,
		exec: local, configured: configured}
	disp, err := ci.NewDispatcher(ci.DispatcherDeps{
		CIDB: db, JobsDB: db, Snapshot: fr.Worktrees().Snapshot, Fence: fr.Leases().Fence,
		StoredSensitivity: storedSensitivity(fr.Jobs()), Executor: ciExecutorFunc(r.run),
		Bus: bus, Attention: attention, Clock: clock,
	})
	if err != nil {
		return nil, err
	}
	disp.OnTerminal(r.checkStreamMarker)
	r.disp = disp
	return r, nil
}

func ciInputsPresent(fr *FleetRuntime, cfg *runtime.Config, bus *events.Bus, attention ci.AttentionPusher, clock runtime.Clock) error {
	missing := ""
	switch {
	case fr == nil:
		missing = "fleet runtime"
	case cfg == nil:
		missing = "config"
	case bus == nil:
		missing = "bus"
	case attention == nil:
		missing = "attention pusher"
	case clock == nil:
		missing = "clock"
	}
	if missing != "" {
		return cascade.Newf(cascade.KindInvalidInput, "daemon: NewCISubsystem requires a non-nil %s", missing)
	}
	if fr.JobsDB() == nil || fr.Worktrees() == nil || fr.Leases() == nil {
		return cascade.New(cascade.KindUnavailable, "daemon: NewCISubsystem: fleet runtime has no jobs stack")
	}
	return nil
}

// Dispatcher returns the one Dispatcher. Handing it out starts the
// runtime: SetExecutor is refused from then on.
func (r *CIRuntime) Dispatcher() *ci.Dispatcher {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	return r.disp
}

// CIDB returns the handle the ci_results tables live on (FleetRuntime's
// cascade.db handle).
func (r *CIRuntime) CIDB() *sql.DB { return r.db }

// Model returns the requirement model for a checkpoint in repoRoot: the Go
// stack when repoRoot holds a go.mod, and the Config ConfigFromRuntime built.
func (r *CIRuntime) Model(repoRoot string) ci.RequirementModel {
	m := ci.RequirementModel{Cfg: r.cfg}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
		m.Stack = "go"
	}
	return m
}

// SetExecutor replaces the local executor. It is refused once the runtime
// has started (Dispatcher was called) and for a nil executor.
func (r *CIRuntime) SetExecutor(e ci.SubJobExecutor) error {
	if e == nil {
		return cascade.New(cascade.KindInvalidInput, "daemon: CIRuntime.SetExecutor requires a non-nil executor")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return cascade.New(cascade.KindConflict, "daemon: CIRuntime.SetExecutor after the CI runtime started")
	}
	r.exec, r.configured = e, nil
	return nil
}

func (r *CIRuntime) run(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	r.mu.Lock()
	e, configured := r.exec, r.configured
	r.mu.Unlock()
	if configured != nil && !configured[sj.Kind] {
		return r.notConfigured(ctx, sj)
	}
	return e.Run(ctx, sj)
}

// notConfigured records sj's explicit not-configured terminal: one ci_run
// (completed, skipped) named like the local executor's stream runs, so a
// redispatch finds and reuses it, and one ci_job naming the reason.
func (r *CIRuntime) notConfigured(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	repoID, name := ci.LocalRepoID(sj.Ref.RepoRoot), "stream-"+sj.Snapshot.AttemptID+"-"+string(sj.Kind)
	if sj.Acceptance {
		name += "-acceptance"
	}
	// Local runs (ci.ReserveLocalRun) take negative ids, descending from -1,
	// so run_id < 0 selects exactly the local runs and never a provider run.
	var runID int64
	err := r.db.QueryRowContext(ctx, `SELECT run_id FROM ci_run WHERE repo_id = ? AND name = ? AND run_id < 0
		ORDER BY run_id LIMIT 1`, repoID, name).Scan(&runID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ci.SubJobResult{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: ci: looking up a not-configured run")
	}
	now := r.clock.Now()
	if runID == 0 {
		if runID, err = ci.ReserveLocalRun(ctx, r.db, repoID, name, now); err != nil {
			return ci.SubJobResult{}, err
		}
	}
	run := ci.Run{RunID: runID, RepoID: repoID, Name: name, HeadSHA: sj.Ref.CheckpointCommit,
		Status: ci.RunStatusCompleted, Conclusion: ci.ConclusionSkipped, CreatedAt: now, UpdatedAt: now}
	job := ci.Job{JobID: runID, RunID: runID, Name: string(sj.Kind) + ": not configured (no [ci.local] command)",
		Status: ci.RunStatusCompleted, Conclusion: ci.ConclusionSkipped, StartedAt: now, FinishedAt: now}
	if err := ci.Upsert(ctx, r.db, run, []ci.Job{job}, nil); err != nil {
		return ci.SubJobResult{}, err
	}
	return ci.SubJobResult{RunID: runID, RepoID: repoID, Passed: false, ExecutorKind: executorKindNotConfigured}, nil
}

// checkStreamMarker raises an attention item for a terminal whose ci_run
// does not carry the via_stream marker (or whose marker cannot be read).
func (r *CIRuntime) checkStreamMarker(ev ci.CIResultEvent) {
	marked, err := ci.RunViaStream(r.ctx, r.db, ev.RunID, ev.RepoID)
	if err == nil && marked {
		return
	}
	item := supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: fmt.Sprintf("ci-daemon:unmarked-stream-run:%s:%d:%d", ev.JobID, ev.RepoID, ev.RunID),
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindGlobal},
	}
	_, _ = r.attention.Push(r.ctx, item) // best effort: a failed push cannot be reported anywhere better
}

type ciExecutorFunc func(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error)

func (f ciExecutorFunc) Run(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	return f(ctx, sj)
}

// storedSensitivity reads the job row's data class (TierOrLocalOnly for an
// unset one, the same rule the checkpoint consumer applies).
func storedSensitivity(store *jobs.Store) func(ctx context.Context, jobID string) (provider.SensitivityTier, error) {
	return func(ctx context.Context, jobID string) (provider.SensitivityTier, error) {
		j, ok, err := store.GetJob(ctx, jobID)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, cascade.Newf(cascade.KindNotFound, "daemon: ci: job %s not found", jobID)
		}
		return provider.TierOrLocalOnly(j.DataClass), nil
	}
}

// newLocalCIExecutor builds the local executor from [ci.local] and the set
// of kinds it has commands for. With no command at all it returns a nil
// executor and an empty set: every kind is then not configured.
func newLocalCIExecutor(paths runtime.PathProvider, cfg *runtime.Config, db *sql.DB, bus *events.Bus,
	clock runtime.Clock) (ci.SubJobExecutor, map[ci.RequirementKind]bool, error) {
	cmds, configured := map[ci.RequirementKind][]string{}, map[ci.RequirementKind]bool{}
	for kind, list := range map[ci.RequirementKind][]string{
		ci.RequirementFormat: cfg.CILocal.Lint, ci.RequirementLint: cfg.CILocal.Lint,
		ci.RequirementCompile: cfg.CILocal.Build, ci.RequirementUnit: cfg.CILocal.Test,
	} {
		if len(list) > 0 {
			cmds[kind], configured[kind] = list, true
		}
	}
	if len(cmds) == 0 {
		return nil, configured, nil
	}
	root := filepath.Join(paths.DataDir(), "ci")
	if err := os.MkdirAll(filepath.Join(root, "stream-runs"), 0o700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: ci: creating the stream run root")
	}
	exec, err := ci.NewLocalSubJobExecutor(ci.LocalExecutorDeps{
		CIDB: db, Events: bus, Clock: clock, Exec: ci.ShellExecutor{}, Commands: cmds,
		Environ: os.Environ(), ExtraEnvKeys: cfg.CILocal.EnvKeys,
		RunRoot: filepath.Join(root, "stream-runs"), ModCache: filepath.Join(root, "modcache"),
		TimeoutPerStep: time.Duration(cfg.CILocal.TimeoutSeconds) * time.Second,
	})
	return exec, configured, err
}
