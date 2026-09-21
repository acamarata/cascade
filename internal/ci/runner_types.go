// Purpose: the local CI runner's plain data types (RunnerStep, StepResult,
// RunnerConfig, RunResult) -- pure structs and their small predicate
// methods, no I/O, no subprocess, no *sql.DB. Split out of runner.go per
// the ticket's own "split by responsibility" task, keeping each file
// under Art.10.3's 300-line cap.
//
// Inputs: none (these are the shapes runner_config.go builds and
// runner.go/runner_exec.go populate).
// Outputs: none.
// Constraints: no bare time.Now (forbidigo) -- Duration fields are always
// caller-computed from an injected runtime.Clock.
// SPORT: internal.ci.RunnerStep/ADDED, internal.ci.StepResult/ADDED,
//
//	internal.ci.RunnerConfig/ADDED, internal.ci.RunResult/ADDED
//	(P1-E25-W5-S51-T5).
//
// Note: RunnerConfig also carries the step ENVIRONMENT (runner_env.go's
// allowlisted set), so a step's environment is a resolved property of the
// run rather than whatever the calling process happened to hold.

package ci

import "time"

// StepKind identifies one of the three fixed local-gate phases. The set is
// closed at exactly these three, matching this ticket's full_desc
// ("lint→test→build") -- there is no extension point for a fourth phase.
type StepKind string

// The three ratified StepKind members, in the fixed sequential order
// stepOrder below always runs them in.
const (
	StepLint  StepKind = "lint"
	StepTest  StepKind = "test"
	StepBuild StepKind = "build"
)

// stepOrder is the local gate's fixed sequential order: lint, then test,
// then build. runner_config.go's BuildRunnerConfig always emits Steps in
// this order; runner.go's engine trusts that order rather than
// re-sorting.
var stepOrder = []StepKind{StepLint, StepTest, StepBuild}

// RunnerStep is one configured command bound to a phase. Named RunnerStep
// (not Step) because normalize.go's Step already names the ci_step
// canonical GitHub Actions record -- distinct concepts, same package.
type RunnerStep struct {
	Kind    StepKind
	Command string
}

// StepResult is one executed RunnerStep's outcome.
type StepResult struct {
	Step     RunnerStep
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	// TimedOut is true when the step exceeded RunnerConfig.TimeoutPerStep.
	TimedOut bool
	// StartErr is set when the command could not even be started (e.g. an
	// absent shell or executable) -- distinct from a non-zero ExitCode,
	// which means the command ran and then failed.
	StartErr error
}

// Passed reports whether this step ran to completion with a zero exit
// code, no timeout, and no start error.
func (r StepResult) Passed() bool {
	return r.StartErr == nil && !r.TimedOut && r.ExitCode == 0
}

// RunnerConfig is one fully-resolved local-gate invocation: the working
// directory, the ordered steps to run, and the per-step timeout.
type RunnerConfig struct {
	RepoRoot string
	Steps    []RunnerStep
	// Env is the complete, already-allowlisted environment every step of
	// this run receives (see runner_env.go). Never the caller's own
	// os.Environ().
	Env            []string
	TimeoutPerStep time.Duration
}

// RunResult is one completed (or halted) local-gate run.
type RunResult struct {
	RunID  int64
	RepoID int64
	Steps  []StepResult
	// FailedStep names the first step that did not pass, or "" when every
	// executed step passed.
	FailedStep StepKind
}

// Passed reports whether every executed step passed (FailedStep == "").
func (r RunResult) Passed() bool {
	return r.FailedStep == ""
}

// The local run's identity (ReserveLocalRun, LocalRepoID) lives in
// runner_ids.go: allocating it needs the store, which this
// types-and-predicates file deliberately has no access to.
