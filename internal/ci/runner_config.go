// Purpose: resolve one [ci.local]-shaped LocalConfig plus a repo root into
// a fully-concrete RunnerConfig -- repo-type auto-detection (go.mod ->
// the Go defaults) fills in whichever of lint/test/build config.toml left
// unset; an unrecognised repo type with no explicit commands fails
// closed (06 §5.20) rather than silently running nothing.
//
// Inputs: LocalConfig (the caller's already-config-parsed lint/test/build
// command lists and timeout -- internal/runtime's ciLocalSection fields,
// copied in by the caller since that type itself is unexported outside
// internal/runtime), a repo root path, and an injected stat function
// (Art.7.1 -- no bare os.Stat in a code path a test exercises without
// hitting the real filesystem).
// Outputs: a RunnerConfig ready for runner.go's engine, or a taxonomy
// error naming exactly what is missing.
// Constraints: fails closed -- an unknown repo type with any of
// lint/test/build still unset after config is an error, never an empty
// step list silently accepted as "nothing to run" (§5.20).
// SPORT: internal.ci.BuildRunnerConfig/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LocalConfig mirrors internal/runtime's ciLocalSection fields (that type
// is unexported outside internal/runtime, so callers copy its exported
// fields in rather than this package importing internal/runtime's
// section type by name).
type LocalConfig struct {
	Lint  []string
	Test  []string
	Build []string
	// EnvKeys are the extra environment-variable NAMES the operator asked
	// to pass through to every step, on top of runner_env.go's fixed
	// allowlist ([ci.local] env).
	EnvKeys        []string
	TimeoutSeconds int
}

// defaultStepTimeout mirrors internal/runtime's defaultCITimeoutSeconds
// (300s) -- duplicated here rather than imported because that constant is
// unexported outside internal/runtime; LocalConfig.TimeoutSeconds is
// normally already resolved by internal/runtime's own default before it
// reaches this package, so this is only a defense-in-depth fallback for a
// caller that builds a LocalConfig directly (e.g. a test).
const defaultStepTimeout = 300 * time.Second

// goDefaultCommands is the repo-type-derived default command set for a
// Go module (go.mod present at the repo root), per this ticket's
// full_desc.
var goDefaultCommands = map[StepKind][]string{
	StepLint:  {"golangci-lint run ./..."},
	StepTest:  {"go test -race ./..."},
	StepBuild: {"go build ./..."},
}

// StatFunc matches os.Stat's signature, injected so BuildRunnerConfig
// never touches the real filesystem in a test that fakes "no go.mod".
type StatFunc func(name string) (os.FileInfo, error)

// BuildRunnerConfig resolves local + repoRoot into a RunnerConfig. A nil
// statFn uses os.Stat (production default); environ is the ambient
// environment the step environment is filtered out of (nil means "read
// this process's own", which stepEnv then filters through the same
// allowlist -- never an unfiltered pass-through).
func BuildRunnerConfig(local LocalConfig, repoRoot string, statFn StatFunc, environ []string) (RunnerConfig, error) {
	if statFn == nil {
		statFn = os.Stat
	}
	if repoRoot == "" {
		repoRoot = "."
	}

	lint, test, build := local.Lint, local.Test, local.Build
	if lint == nil || test == nil || build == nil {
		defaults, err := detectDefaults(repoRoot, statFn, lint == nil && test == nil && build == nil)
		if err != nil {
			return RunnerConfig{}, err
		}
		if lint == nil {
			lint = defaults[StepLint]
		}
		if test == nil {
			test = defaults[StepTest]
		}
		if build == nil {
			build = defaults[StepBuild]
		}
	}

	steps := assembleSteps(lint, test, build)
	if len(steps) == 0 {
		return RunnerConfig{}, cascade.New(cascade.KindInvalidInput,
			"ci: no lint/test/build commands configured; set [ci.local] in config.toml or run from a recognized project root")
	}

	timeout := time.Duration(local.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultStepTimeout
	}
	return RunnerConfig{
		RepoRoot:       repoRoot,
		Steps:          steps,
		Env:            AllowedEnv(environ, local.EnvKeys),
		TimeoutPerStep: timeout,
	}, nil
}

// ValidateRepoRoot refuses a --repo that is not an existing directory
// holding a .git entry. Without the check, `cascade ci run --repo /tpm`
// (a typo) silently runs the configured commands in a directory that is
// not the repository the operator meant, and records the result under a
// repo_id derived from that wrong path. .git may be a directory (a normal
// clone) or a file (a worktree or submodule), so only its presence is
// asserted, never its kind.
func ValidateRepoRoot(repoRoot string, statFn StatFunc) error {
	if statFn == nil {
		statFn = os.Stat
	}
	info, err := statFn(repoRoot)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "ci: --repo %q is not readable", repoRoot)
	}
	if info == nil {
		// Only an injected StatFunc can report success with no FileInfo
		// (os.Stat never does). Refuse rather than dereference it.
		return cascade.Newf(cascade.KindInternal, "ci: stat of %q returned no file info", repoRoot)
	}
	if !info.IsDir() {
		return cascade.Newf(cascade.KindInvalidInput, "ci: --repo %q is not a directory", repoRoot)
	}
	if _, err := statFn(filepath.Join(repoRoot, ".git")); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"ci: --repo %q holds no .git; the local gate runs against a repository checkout", repoRoot)
	}
	return nil
}

// detectDefaults probes repoRoot for a recognized project file and
// returns its default per-StepKind commands. allUnset is true only when
// config.toml configured NONE of lint/test/build -- an unrecognized repo
// type is then a hard, fail-closed error (§5.20); when at least one of
// the three was explicitly configured, an unrecognized type simply
// contributes no defaults for the still-unset ones, and
// BuildRunnerConfig's own "no commands configured" check catches a truly
// empty result.
func detectDefaults(repoRoot string, statFn StatFunc, allUnset bool) (map[StepKind][]string, error) {
	if _, err := statFn(filepath.Join(repoRoot, "go.mod")); err == nil {
		return goDefaultCommands, nil
	}
	if allUnset {
		return nil, cascade.Newf(cascade.KindUnsupported,
			"ci: no recognized project file (go.mod) found in %s and no [ci.local] commands configured", repoRoot)
	}
	return map[StepKind][]string{}, nil
}

// assembleSteps flattens lint/test/build command lists into the fixed
// stepOrder sequence, one Step per command (a phase may list more than
// one command, e.g. two lint passes).
func assembleSteps(lint, test, build []string) []RunnerStep {
	byKind := map[StepKind][]string{StepLint: lint, StepTest: test, StepBuild: build}
	var steps []RunnerStep
	for _, kind := range stepOrder {
		for _, cmd := range byKind[kind] {
			steps = append(steps, RunnerStep{Kind: kind, Command: cmd})
		}
	}
	return steps
}
