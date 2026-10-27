// Purpose (this file): the shared entry points of the local CI gate --
// RunLocal and StatusLocal -- extracted from `cascade ci run|status` so a
// second surface (the in-process MCP methods in mcp_methods.go) runs the
// SAME code path the CLI does, never a copy of it.
//
// Inputs: a CmdDeps, a repository root (RunLocal) or a row limit
// (StatusLocal).
// Outputs: the RunResult of a recorded local run, or the most recent
// RunSummary rows.
// Constraints: behaviour-neutral with respect to the CLI. RunLocal
// consults the never-pay policy BEFORE any store is opened or any step is
// run, and neither function prints anything: rendering belongs to the
// caller. StatusLocal is a read of the ci_results domain and consults no
// policy, exactly as `ci status` always has.

package ci

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StepFilter narrows a local run to some of its steps. The zero value runs
// every configured step; setting several flags runs their union, exactly
// as `ci run --lint-only --test-only` does.
type StepFilter struct {
	LintOnly  bool
	TestOnly  bool
	BuildOnly bool
}

// flags maps the filter onto the flag struct filterOnlySteps reads, so the
// CLI and the MCP surface share one selection rule.
func (f StepFilter) flags() ciRunFlags {
	return ciRunFlags{LintOnly: f.LintOnly, TestOnly: f.TestOnly, BuildOnly: f.BuildOnly}
}

// RunLocal runs the local lint/test/build gate for the checkout at
// repoRoot and records the result. An empty repoRoot means the process's
// current directory (the CLI's --repo default).
//
// Order is part of the contract: resolve and validate the checkout, load
// the step sequence, consult the never-pay policy, and only then open the
// stores and run a step. A repository whose CI belongs on hosted Actions
// therefore has nothing recorded against it, and a path that is not a
// checkout runs nothing.
//
// A step that fails is a RESULT (RunResult.Passed is false), not an error;
// the error return is for a run that could not be made at all.
func RunLocal(ctx context.Context, deps CmdDeps, repoRoot string, only StepFilter) (RunResult, error) {
	return runLocal(ctx, deps, repoRoot, only, openRunDeps)
}

// runLocal is RunLocal over a store opener, so the MCP surface can run the
// SAME sequence with a tolerant opener (see openRunDepsShared) while the
// CLI keeps the strict one.
func runLocal(ctx context.Context, deps CmdDeps, repoRoot string, only StepFilter, open runDepsOpener) (RunResult, error) {
	paths, err := deps.resolvePaths()
	if err != nil {
		return RunResult{}, err
	}
	absRoot, err := resolveCheckout(repoRoot)
	if err != nil {
		return RunResult{}, err
	}
	rcfg, err := loadRunnerConfig(ctx, deps, paths.ConfigPath(), absRoot, only.flags())
	if err != nil {
		return RunResult{}, err
	}
	// The same resolved identity feeds the never-pay guard AND the
	// completed-run event a [ci.watch] subscriber matches against --
	// resolved once, never twice.
	rcfg.OwnerRepo = ownerRepoFor(ctx, rcfg)
	if err := guardLocalRun(ctx, deps.Routes, rcfg.OwnerRepo); err != nil {
		return RunResult{}, err
	}

	runDeps, closeStores, err := open(ctx, paths.DataDir(), deps.Clock)
	if err != nil {
		return RunResult{}, err
	}
	defer closeStores()

	repoID, repoName := LocalRepoID(absRoot), filepath.Base(absRoot)
	runID, err := ReserveLocalRun(ctx, runDeps.DB, repoID, repoName, deps.Clock.Now())
	if err != nil {
		return RunResult{}, err
	}
	return Execute(ctx, runDeps, rcfg, runID, repoID, repoName)
}

// runDepsOpener opens the stores a run writes through (openRunDeps's shape).
type runDepsOpener func(ctx context.Context, dataDir string, clock runtime.Clock) (Deps, func(), error)

// openRunDepsShared is openRunDeps for a process that may already hold the
// embedded store's write lock -- the `cascade mcp serve` process holds it
// for its own recall index, and a daemon holds it when one is running.
// When, and only when, the embedded write store is refused as held
// (KindConflict), the run proceeds over the results database alone: the run
// and its jobs and steps are still recorded in ci_results (so ci.status
// shows them), but the best-effort step journal and the run-completed event
// are skipped. Every other failure is returned unchanged.
func openRunDepsShared(ctx context.Context, dataDir string, clock runtime.Clock) (Deps, func(), error) {
	return sharedOpener(openRunDeps)(ctx, dataDir, clock)
}

// sharedOpener wraps strict with the fallback described on
// openRunDepsShared. Split out so the fail-closed half can be tested with a
// strict opener that fails in ways the real one rarely does.
func sharedOpener(strict runDepsOpener) runDepsOpener {
	return func(ctx context.Context, dataDir string, clock runtime.Clock) (Deps, func(), error) {
		deps, closeStores, err := strict(ctx, dataDir, clock)
		if err == nil {
			return deps, closeStores, nil
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
			return Deps{}, func() {}, err
		}
		db, dbErr := openResultsDB(ctx, dataDir, clock)
		if dbErr != nil {
			return Deps{}, func() {}, dbErr
		}
		return Deps{DB: db, Clock: clock, Exec: ShellExecutor{}}, func() { _ = db.Close() }, nil
	}
}

// StatusLocal returns the limit most recently updated CI runs, both
// source=github-actions and source=local, newest first. It polls nothing
// and spends nothing, so there is no paid direction to guard. A
// non-positive limit takes ListRuns's default.
func StatusLocal(ctx context.Context, deps CmdDeps, limit int) ([]RunSummary, error) {
	paths, err := deps.resolvePaths()
	if err != nil {
		return nil, err
	}
	db, err := openResultsDB(ctx, paths.DataDir(), deps.Clock)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return ListRuns(ctx, db, limit)
}
