package daemon

// Purpose: the daemon's one streaming CI dispatcher (P1-CI-10,
// contract:ci-daemon-composition). NewCISubsystem builds the only
// ci.Dispatcher a daemon holds, over FleetRuntime's one cascade.db handle
// (the ci_results schema is applied to it, no second handle is opened), the
// LeaseManager's Fence and the WorktreeManager's Snapshot. The requirement
// model's Config comes from ci.ConfigFromRuntime only.
//
// The executor is the resolver-backed one (P1-CI-03), installed through
// SetExecutor before the runtime starts: one ci.Resolver over
// FleetRuntime.Fleet(), a RunnerSet holding only the local runner (the
// clean-checkout executor built from [ci.local]: lint and format run the
// lint list, compile the build list, unit the test list) and no GitHub
// reader (no HTTP doer or ci-poll egress engine reaches NewCISubsystem, so
// the hosted step is refused). Every kind goes to that executor. A kind no
// executor may run (integration, architecture and security always; any kind
// whose [ci.local] list is empty) ends as the resolving executor's explicit
// non-pass terminal (completed/skipped, executor kind "unresolved",
// Passed=false), which retires P1-CI-10's not-configured path (T0
// P1-BF-R294). Resolver refusals are journaled to the daemon log.
//
// Source trust: the dispatcher only receives checkpoints of jobs leased from
// this controller's own job store on repositories registered with it, the
// owner's repositories (provider.ProvenanceTrusted). A job missing from the
// store is an error, never trusted.
//
// Every terminal sub-job is checked against its ci_run_stream marker
// (ci.RunViaStream). A terminal whose run is not marked as a stream run is
// an integrity fault: it raises one attention item.
//
// SPORT: internal.daemon.CIRuntime/ADDED, internal.daemon.NewCISubsystem/ADDED (P1-CI-10;
// resolver composition P1-CI-03).

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	// exec runs every kind (SetExecutor installs it before the runtime starts).
	exec    ci.SubJobExecutor
	started bool
}

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
	exec, err := newResolvingCIExecutor(fr, cfg, db, bus, clock)
	if err != nil {
		return nil, err
	}
	r := &CIRuntime{ctx: ctx, db: db, cfg: ci.ConfigFromRuntime(*cfg), attention: attention, clock: clock}
	if err := r.SetExecutor(exec); err != nil {
		return nil, err
	}
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
	r.exec = e
	return nil
}

func (r *CIRuntime) run(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	r.mu.Lock()
	e := r.exec
	r.mu.Unlock()
	return e.Run(ctx, sj)
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

// newLocalCIExecutor builds the local executor from [ci.local]. With no
// command at all it returns nil: the local runner then refuses every kind.
func newLocalCIExecutor(paths runtime.PathProvider, cfg *runtime.Config, db *sql.DB, bus *events.Bus,
	clock runtime.Clock) (ci.SubJobExecutor, error) {
	cmds := map[ci.RequirementKind][]string{}
	for kind, list := range map[ci.RequirementKind][]string{
		ci.RequirementFormat: cfg.CILocal.Lint, ci.RequirementLint: cfg.CILocal.Lint,
		ci.RequirementCompile: cfg.CILocal.Build, ci.RequirementUnit: cfg.CILocal.Test,
	} {
		if len(list) > 0 {
			cmds[kind] = list
		}
	}
	if len(cmds) == 0 {
		return nil, nil
	}
	root := filepath.Join(paths.DataDir(), "ci")
	if err := os.MkdirAll(filepath.Join(root, "stream-runs"), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: ci: creating the stream run root")
	}
	return ci.NewLocalSubJobExecutor(ci.LocalExecutorDeps{
		CIDB: db, Events: bus, Clock: clock, Exec: ci.ShellExecutor{}, Commands: cmds,
		Environ: os.Environ(), ExtraEnvKeys: cfg.CILocal.EnvKeys,
		RunRoot: filepath.Join(root, "stream-runs"), ModCache: filepath.Join(root, "modcache"),
		TimeoutPerStep: time.Duration(cfg.CILocal.TimeoutSeconds) * time.Second,
	})
}

// newResolvingCIExecutor builds the resolver-backed executor: one Resolver
// over FleetRuntime.Fleet(), RunnerSet{local} and no GitHub reader.
func newResolvingCIExecutor(fr *FleetRuntime, cfg *runtime.Config, db *sql.DB, bus *events.Bus,
	clock runtime.Clock) (ci.SubJobExecutor, error) {
	local, err := newLocalCIExecutor(fr.res.paths, cfg, db, bus, clock)
	if err != nil {
		return nil, err
	}
	ci.SetToolchainVersions(cfg.CIToolchainVersions)
	runners := ciRunners(local)
	deps := ci.ResolverDeps{Runners: runners, Fingerprint: ci.ComputeToolchainFingerprint,
		Refusals: journalCIRefusal, Clock: clock}
	if f := fr.Fleet(); f != nil {
		deps.Fleet = f
	}
	res, err := ci.NewResolver(deps)
	if err != nil {
		return nil, err
	}
	return ci.NewResolvingSubJobExecutor(res, runners, deps.Fleet, ci.ResolvingDeps{
		CIDB: db, Clock: clock, Fingerprint: ci.ComputeToolchainFingerprint, SourceTrust: storedSourceTrust(fr.Jobs()),
	}), nil
}

// ciRunners is the production runner registry: exactly the local runner.
// Node and github-actions runners are P1-NODE-08/-09's.
func ciRunners(local ci.SubJobExecutor) ci.RunnerSet {
	return ci.RunnerSet{ci.ExecutorLocal: ciLocalRunner{exec: local}}
}

// ciLocalRunner runs the local kind on the [ci.local] executor; with no
// command configured it refuses every kind with ci.ErrNoCapableExecutor.
type ciLocalRunner struct{ exec ci.SubJobExecutor }

func (l ciLocalRunner) Run(ctx context.Context, _ ci.ResolvedExecutor, sj ci.SubJob) (ci.SubJobResult, error) {
	if l.exec == nil {
		return ci.SubJobResult{}, cascade.Wrapf(cascade.KindUnavailable, ci.ErrNoCapableExecutor,
			"daemon: ci: [ci.local] has no command, so %s cannot run locally", sj.Kind)
	}
	return l.exec.Run(ctx, sj)
}

// journalCIRefusal writes one resolver refusal to the daemon log.
func journalCIRefusal(_ context.Context, r ci.Refusal) {
	slog.Default().Info("ci: executor step refused", "job", r.JobID, "kind", string(r.Kind),
		"step", string(r.Step), "reason", r.Reason, "at", r.At)
}

// storedSourceTrust reports a job of this controller's own store as
// trusted (the owner's repository); a job it cannot find is an error.
func storedSourceTrust(store *jobs.Store) func(ctx context.Context, sj ci.SubJob) (provider.Provenance, error) {
	return func(ctx context.Context, sj ci.SubJob) (provider.Provenance, error) {
		_, ok, err := store.GetJob(ctx, sj.Ref.JobID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", cascade.Newf(cascade.KindNotFound, "daemon: ci: job %s is not in this controller's store", sj.Ref.JobID)
		}
		return provider.ProvenanceTrusted, nil
	}
}
