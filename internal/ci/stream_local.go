// Purpose (this file): the local SubJobExecutor: it materializes the
// snapshot tree of a sub-job into a fresh run directory and runs the kind's
// commands there through Execute, so a stream or acceptance run executes
// exactly the bound tree and never the live worktree (a clean-room
// executor).
//
// Inputs: a SubJob (its Snapshot.TreeHash names the tree), the per-kind
// commands, the ambient environment (injected, never read from os.Environ
// by this file), a run root and the shared module cache directory.
// Outputs: a SubJobResult whose RunID/RepoID select the ci_run row Execute
// wrote, with ci_run.name = streamRunName so a re-run finds it.
// Constraints (R18 B1, M6, N6): the run directory is built from the tree
// object only (`git read-tree` into a private index, then
// `git checkout-index`); the controller -- not the job -- first runs
// `go mod download` and `go mod verify` there with the ambient environment
// plus GOTOOLCHAIN=local (a verify failure refuses the run); the run itself
// gets a fresh per-run HOME, TMPDIR and GOCACHE, GOFLAGS exactly
// -mod=readonly, GOPROXY=off, GOSUMDB=off, GOTOOLCHAIN=local and the shared
// GOMODCACHE, applied after AllowedEnv so they win over [ci.local] env
// keys, and go telemetry is switched off in that HOME before the first go
// command (stream_telemetry.go); a module missing from the cache fails the
// run. The same-uid write residual on the shared cache is disclosed in docs/ci/streaming.md and is
// never a trust input. The executor is idempotent per (attempt, kind): a
// completed run is returned as it is and an interrupted one is resumed on
// its reserved ci_run id, so Resume never writes a second ci_run.
// SPORT: internal.ci.NewLocalSubJobExecutor/ADDED,
//
//	internal.ci.LocalExecutorDeps/ADDED, internal.ci.Environment/ADDED
//	(P1-CI-01).

package ci

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// IsolationCleanCheckoutSameUID is the Environment.IsolationClass of the
// local executor: a clean checkout of the bound tree, run as the same user.
const IsolationCleanCheckoutSameUID = "clean-checkout-same-uid"

// executorKindLocal is the SubJobResult.ExecutorKind of this executor.
const executorKindLocal = "local"

// Environment describes the isolation one local run executed under: the
// class, the run directory and the exact environment the run received.
type Environment struct {
	IsolationClass string
	RunDir         string
	Env            []string
}

// LocalExecutorDeps carries the local executor's inputs. CIDB, Clock, Exec,
// Commands, RunRoot and ModCache are required.
type LocalExecutorDeps struct {
	CIDB    *sql.DB
	Events  *events.Bus
	Journal JournalStore
	Clock   runtime.Clock
	Exec    Executor
	// Commands lists, per requirement kind, the shell commands the run
	// executes. "{targets}" in a command expands to the plan's affected
	// targets, or "./..." when the plan is full.
	Commands map[RequirementKind][]string
	// Environ is the ambient environment snapshot; ExtraEnvKeys are the
	// [ci.local] env names passed through AllowedEnv.
	Environ, ExtraEnvKeys []string
	// RunRoot holds the per-run directories; ModCache is the shared,
	// controller-populated GOMODCACHE.
	RunRoot, ModCache string
	// Populate is the controller's module populate-and-verify step; nil
	// means `go mod download` then `go mod verify`.
	Populate func(ctx context.Context, dir string, env []string) error
	// OnEnvironment, when set, receives the Environment of every run.
	OnEnvironment  func(Environment)
	TimeoutPerStep time.Duration
}

type localExecutor struct{ d LocalExecutorDeps }

// NewLocalSubJobExecutor validates d and returns the local executor.
func NewLocalSubJobExecutor(d LocalExecutorDeps) (SubJobExecutor, error) {
	missing := ""
	switch {
	case d.CIDB == nil:
		missing = "CIDB"
	case d.Clock == nil:
		missing = "Clock"
	case d.Exec == nil:
		missing = "Exec"
	case len(d.Commands) == 0:
		missing = "Commands"
	case d.RunRoot == "":
		missing = "RunRoot"
	case d.ModCache == "":
		missing = "ModCache"
	}
	if missing != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "ci: NewLocalSubJobExecutor requires %s", missing)
	}
	return &localExecutor{d: d}, nil
}

// streamRunName is the ci_run.name of one (attempt, kind, acceptance) run.
func streamRunName(sj SubJob) string {
	name := "stream-" + sj.Snapshot.AttemptID + "-" + string(sj.Kind)
	if sj.Acceptance {
		name += "-acceptance"
	}
	return name
}

// Run executes sj in a fresh checkout of its snapshot tree.
func (l *localExecutor) Run(ctx context.Context, sj SubJob) (SubJobResult, error) {
	if !sj.Kind.Valid() || sj.Ref.RepoRoot == "" || !objectIDPattern.MatchString(sj.Snapshot.TreeHash) {
		return SubJobResult{}, cascade.New(cascade.KindInvalidInput, "ci: local executor: invalid sub-job")
	}
	repoID, name := LocalRepoID(sj.Ref.RepoRoot), streamRunName(sj)
	runID, completed, passed, err := l.findRun(ctx, repoID, name)
	if err != nil {
		return SubJobResult{}, err
	}
	res := SubJobResult{RunID: runID, RepoID: repoID, Passed: passed, ExecutorKind: executorKindLocal}
	if completed {
		return res, nil
	}
	runDir, err := os.MkdirTemp(l.d.RunRoot, "run-")
	if err != nil {
		return SubJobResult{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: creating the run directory")
	}
	defer func() {
		if rerr := removeRunDir(runDir); rerr != nil {
			slog.Default().Warn("ci: local executor could not remove a run directory", "dir", runDir, "error", rerr)
		}
	}()
	cfg, err := l.prepare(ctx, sj, runDir)
	if err != nil {
		return SubJobResult{}, err
	}
	if runID == 0 {
		if res.RunID, err = ReserveLocalRun(ctx, l.d.CIDB, repoID, name, l.d.Clock.Now()); err != nil {
			return SubJobResult{}, err
		}
	}
	deps := Deps{DB: l.d.CIDB, Events: l.d.Events, Journal: l.d.Journal, Clock: l.d.Clock, Exec: l.d.Exec, ViaStream: true}
	rr, err := Execute(ctx, deps, cfg, res.RunID, repoID, name)
	res.Passed = rr.Passed()
	return res, err
}

// removeRunDir removes a finished run's directory with one RemoveAll. The
// clean room turns go telemetry off (disableGoTelemetry), so no toolchain
// process writes under the run after its command exits and no retry is
// needed. A failure is returned for the caller to log; a directory that
// could not be removed is left for the run root's owner to sweep.
func removeRunDir(dir string) error {
	return os.RemoveAll(dir)
}

// prepare materializes the tree, runs the controller's populate step and
// builds the run's RunnerConfig.
func (l *localExecutor) prepare(ctx context.Context, sj SubJob, runDir string) (RunnerConfig, error) {
	treeDir, err := l.materialize(ctx, sj, runDir)
	if err != nil {
		return RunnerConfig{}, err
	}
	steps, err := l.steps(sj)
	if err != nil {
		return RunnerConfig{}, err
	}
	if err := l.populate(ctx, treeDir); err != nil {
		return RunnerConfig{}, err
	}
	env, err := cleanRoomEnv(l.d.Environ, l.d.ExtraEnvKeys, runDir, l.d.ModCache)
	if err != nil {
		return RunnerConfig{}, err
	}
	if l.d.OnEnvironment != nil {
		l.d.OnEnvironment(Environment{IsolationClass: IsolationCleanCheckoutSameUID, RunDir: runDir, Env: env})
	}
	timeout := l.d.TimeoutPerStep
	if timeout <= 0 {
		timeout = defaultStepTimeout
	}
	return RunnerConfig{RepoRoot: treeDir, Steps: steps, Env: env, TimeoutPerStep: timeout}, nil
}

// findRun looks up the ci_run a previous attempt of this (attempt, kind)
// reserved: its id (0 if none), whether it completed with its ci_job row,
// and whether it passed.
func (l *localExecutor) findRun(ctx context.Context, repoID int64, name string) (runID int64, completed, passed bool, err error) {
	var status, conclusion string
	var hasJob bool
	row := l.d.CIDB.QueryRowContext(ctx, `
		SELECT r.run_id, r.status, r.conclusion,
		       EXISTS(SELECT 1 FROM `+tableJob+` j WHERE j.job_id = r.run_id AND j.status = ?)
		FROM `+tableRun+` r WHERE r.repo_id = ? AND r.name = ? AND r.run_id < 0 ORDER BY r.run_id LIMIT 1`,
		string(RunStatusCompleted), repoID, name)
	switch err := row.Scan(&runID, &status, &conclusion, &hasJob); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, false, nil
	case err != nil:
		return 0, false, false, cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: looking up the reserved run")
	}
	return runID, status == string(RunStatusCompleted) && hasJob, conclusion == string(ConclusionSuccess), nil
}

// materialize builds the run's checkout from the snapshot tree object only.
func (l *localExecutor) materialize(ctx context.Context, sj SubJob, runDir string) (string, error) {
	treeDir := filepath.Join(runDir, "tree")
	if err := os.MkdirAll(treeDir, 0o700); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: creating the checkout directory")
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(runDir, "index")}
	if _, err := streamGitEnv(ctx, sj.Ref.RepoRoot, env, "read-tree", sj.Snapshot.TreeHash); err != nil {
		return "", cascade.Wrapf(cascade.KindIntegrity, errors.Join(ErrTreeHashMismatch, err),
			"ci: local executor: tree %s is not in the object store", sj.Snapshot.TreeHash)
	}
	prefix := "--prefix=" + filepath.ToSlash(treeDir) + "/"
	if _, err := streamGitEnv(ctx, sj.Ref.RepoRoot, env, "checkout-index", "-a", "-f", prefix); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: checking out the snapshot tree")
	}
	if sj.Acceptance {
		for _, p := range sj.Ref.Untracked {
			if _, err := os.Lstat(filepath.Join(treeDir, filepath.FromSlash(p))); err != nil {
				return "", cascade.Wrapf(cascade.KindIntegrity, ErrTreeHashMismatch,
					"ci: local executor: declared untracked path %q is not in the committed tree an acceptance run executes", p)
			}
		}
	}
	return treeDir, nil
}

// steps expands the kind's commands for the plan's targets.
func (l *localExecutor) steps(sj SubJob) ([]RunnerStep, error) {
	cmds := l.d.Commands[sj.Kind]
	if len(cmds) == 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "ci: local executor: no commands configured for kind %q", sj.Kind)
	}
	targets, err := targetArgs(sj.Plan)
	if err != nil {
		return nil, err
	}
	out := make([]RunnerStep, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, RunnerStep{Kind: stepKindFor(sj.Kind), Command: strings.ReplaceAll(c, "{targets}", targets)})
	}
	return out, nil
}
