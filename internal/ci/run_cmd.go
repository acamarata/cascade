// Purpose (this file): `cascade ci run` -- the local lint/test/build gate's
// cobra command, its flags, and the four steps that stand between an
// invocation and a recorded result: resolve the checkout, resolve
// [ci.local] into a concrete step sequence, consult the never-pay policy,
// then execute. Split out of runner_cmd.go (which keeps the seams `ci
// status` shares) purely to hold every file under Art.10.3's 300-line cap,
// the same R-14.117 remedy runner_view.go and status_cmd.go already apply
// within this ticket.
//
// Inputs: cobra args/flags (--lint-only/--test-only/--build-only/--repo)
// plus the CmdDeps injected at construction.
// Outputs: process output via internal/output.Writer; a non-nil error --
// which main.go maps to a non-zero exit -- when any step fails or the
// policy refuses.
// Constraints: fully non-interactive (06 §5.8) and daemonless (06 §2).
// SPORT: internal.ci.runCIRun/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ciRunFlags holds `ci run`'s flag values.
type ciRunFlags struct {
	LintOnly  bool
	TestOnly  bool
	BuildOnly bool
	Repo      string
}

// newCIRunCmd builds `cascade ci run`.
func newCIRunCmd(deps CmdDeps) *cobra.Command {
	var flags ciRunFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the local lint/test/build gate and record the result",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCIRun(cmd, deps, flags)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&flags.LintOnly, "lint-only", false, "run only the lint step")
	f.BoolVar(&flags.TestOnly, "test-only", false, "run only the test step")
	f.BoolVar(&flags.BuildOnly, "build-only", false, "run only the build step")
	f.StringVar(&flags.Repo, "repo", "", "repository root (default: the current directory)")
	return cmd
}

// resolveRepoRoot returns flagValue if set, else the process's current
// directory.
func resolveRepoRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: resolve the current directory")
	}
	return cwd, nil
}

// filterOnlySteps applies --lint-only/--test-only/--build-only, or
// returns steps unchanged when none of the three flags is set.
func filterOnlySteps(steps []RunnerStep, flags ciRunFlags) []RunnerStep {
	if !flags.LintOnly && !flags.TestOnly && !flags.BuildOnly {
		return steps
	}
	out := make([]RunnerStep, 0, len(steps))
	for _, s := range steps {
		if (flags.LintOnly && s.Kind == StepLint) ||
			(flags.TestOnly && s.Kind == StepTest) ||
			(flags.BuildOnly && s.Kind == StepBuild) {
			out = append(out, s)
		}
	}
	return out
}

// runCIRun implements `ci run`'s RunE: resolve config and the repo root,
// check the never-pay policy actually routes this repository to the local
// gate, build the runner config, open the embedded stores, execute, print
// the result, and exit non-zero when any step failed.
func runCIRun(cmd *cobra.Command, deps CmdDeps, flags ciRunFlags) error {
	ctx := cmd.Context()
	paths, err := deps.resolvePaths()
	if err != nil {
		return err
	}
	absRoot, err := resolveCheckout(flags.Repo)
	if err != nil {
		return err
	}
	rcfg, err := loadRunnerConfig(ctx, deps, paths.ConfigPath(), absRoot, flags)
	if err != nil {
		return err
	}
	// The policy gate comes BEFORE any store is opened or any step runs:
	// a repository whose CI belongs on hosted Actions must not have a
	// local run recorded against it at all.
	if err := guardLocalRun(ctx, deps.Routes, ownerRepoFor(ctx, rcfg)); err != nil {
		return err
	}

	runDeps, closeStores, err := openRunDeps(ctx, paths.DataDir(), deps.Clock)
	if err != nil {
		return err
	}
	defer closeStores()

	repoID, repoName := LocalRepoID(absRoot), filepath.Base(absRoot)
	runID, err := ReserveLocalRun(ctx, runDeps.DB, repoID, repoName, deps.Clock.Now())
	if err != nil {
		return err
	}
	result, err := Execute(ctx, runDeps, rcfg, runID, repoID, repoName)
	if err != nil {
		return err
	}
	if writeErr := outputWriter(cmd).Result(newRunResultView(result)); writeErr != nil {
		return writeErr
	}
	if !result.Passed() {
		return cascade.Newf(cascade.KindConflict, "ci: step %q failed", result.FailedStep)
	}
	return nil
}

// resolveCheckout resolves the repository root to an absolute path and
// refuses one that is not a checkout (see ValidateRepoRoot).
func resolveCheckout(flagValue string) (string, error) {
	repoRoot, err := resolveRepoRoot(flagValue)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "ci: resolve the repo root")
	}
	if err := ValidateRepoRoot(absRoot, nil); err != nil {
		return "", err
	}
	return absRoot, nil
}

// loadRunnerConfig reads [ci.local], resolves the step sequence for
// absRoot, and applies the --lint-only/--test-only/--build-only filter.
func loadRunnerConfig(ctx context.Context, deps CmdDeps, configPath, absRoot string, flags ciRunFlags) (RunnerConfig, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: configPath, Getenv: deps.Getenv, Environ: deps.Environ})
	if err != nil {
		return RunnerConfig{}, err
	}
	var environ []string
	if deps.Environ != nil {
		environ = deps.Environ()
	}
	rcfg, err := BuildRunnerConfig(LocalConfig{
		Lint: cfg.CILocal.Lint, Test: cfg.CILocal.Test, Build: cfg.CILocal.Build,
		EnvKeys: cfg.CILocal.EnvKeys, TimeoutSeconds: cfg.CILocal.TimeoutSeconds,
	}, absRoot, nil, environ)
	if err != nil {
		return RunnerConfig{}, err
	}
	rcfg.Steps = filterOnlySteps(rcfg.Steps, flags)
	if len(rcfg.Steps) == 0 {
		return RunnerConfig{}, cascade.New(cascade.KindInvalidInput, "ci: no steps selected to run")
	}
	return rcfg, nil
}

// ownerRepoFor names the repository the never-pay policy routes, read from
// the checkout's own `origin` remote. A checkout with no origin (or one
// whose URL is not a recognisable owner/repo) yields "": guardLocalRun
// treats that as local-gate-eligible, because a tree with no GitHub remote
// has no hosted Actions to be routed to. The lookup runs through the same
// allowlisted environment and bounded timeout as any other step, so it
// cannot hang the command or see a credential a step could not.
func ownerRepoFor(ctx context.Context, rcfg RunnerConfig) string {
	res := ShellExecutor{}.Run(ctx, ExecRequest{
		WorkDir: rcfg.RepoRoot, Command: "git remote get-url origin",
		Env: rcfg.Env, Timeout: gitRemoteTimeout,
	})
	if res.StartErr != nil || res.TimedOut || res.ExitCode != 0 {
		return ""
	}
	ownerRepo, ok := parseOwnerRepo(res.Stdout)
	if !ok {
		return ""
	}
	return ownerRepo
}
