// Purpose: the local CI runner's sequential lint->test->build engine
// (Execute): runs RunnerConfig's steps in order via an injected Executor, halting at
// the first failure, journaling each step's start/pass-or-fail outcome
// through the N/S-30.T1 entity-journal capability, writing the completed
// (or halted) result to the ci_results domain with source='local', and
// publishing a C/S-04.T3 domain event so a subscriber can observe it.
//
// Inputs: a RunnerConfig (runner_config.go) plus Deps -- the real *sql.DB,
// events.Bus, JournalStore and runtime.Clock a production caller
// (runner_cmd.go) constructs, or the fakes runner_test.go substitutes.
// Outputs: a RunResult and a taxonomy error only for a Deps precondition
// failure or a ci_results write failure -- a FAILING step is not itself a
// Go error, it is a RunResult with FailedStep set and Passed()==false;
// runner_cmd.go maps that to the process's non-zero exit.
// Constraints: no bare time.Now (forbidigo) -- every timestamp comes from
// deps.Clock. Events and Journal are optional (nil skips that one side
// effect only); DB, Clock, and Exec are required.
// SPORT: internal.ci.Execute/ADDED, internal.ci.Deps/ADDED,
//
//	internal.ci.JournalStore/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EventNamespace is the events.Bus namespace this package's completed-run
// events publish to.
const EventNamespace = "ci_results"

// EventKindRunCompleted is minted locally for one completed local run --
// events.EventKind is deliberately OPEN (its own doc comment: "many
// independently-owned future producers ... mint their own EventKind
// values with no need for an amendment"), so this needs no change to
// internal/events itself.
const EventKindRunCompleted events.EventKind = "ci.run.completed"

// JournalStore is the seam Execute journals step lifecycle through:
// journal.Store's Append only, duck-typed exactly like
// internal/conversation/journal.go's own identical JournalStore
// interface, so *journal.SQLiteStore satisfies it with no adapter.
type JournalStore interface {
	Append(ctx context.Context, entityID string, kind journal.Kind, operationID string, payload json.RawMessage) (journal.Entry, error)
}

// stepJournalPayload discriminates the two step-lifecycle entries this
// package journals -- journal.Kind's closed 8-member enum (R-21.216)
// supplies KindIntent (step start) and KindAck (step result); Result
// distinguishes pass from fail INSIDE the Ack payload, matching
// internal/conversation/journal.go's identical discriminator-inside-
// Payload pattern for the same closed-enum constraint.
type stepJournalPayload struct {
	Step   StepKind `json:"step"`
	Result string   `json:"result,omitempty"`
}

// Deps carries every external input Execute needs. DB, Clock, and Exec are
// required; Events and Journal are optional (nil disables that one side
// effect, never the ci_results write itself).
type Deps struct {
	DB      *sql.DB
	Events  *events.Bus
	Journal JournalStore
	Clock   runtime.Clock
	Exec    Executor
}

// journalEntityID names one local run's journal entity, matching
// internal/conversation/journal.go's per-domain-entity convention.
func journalEntityID(runID, repoID int64) string {
	return fmt.Sprintf("ci-run-%d-%d", repoID, runID)
}

// validate checks Execute's required Deps fields, fail-closed.
func (d Deps) validate() error {
	if d.DB == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: Execute requires a non-nil DB")
	}
	if d.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: Execute requires a non-nil Clock")
	}
	if d.Exec == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: Execute requires a non-nil Executor")
	}
	return nil
}

// Execute runs cfg's steps in their fixed order, halting at the first
// failing step. runID/repoID identify this run in the ci_results domain
// (see NewLocalRunID/LocalRepoID); repoName is ci_run.name.
func Execute(ctx context.Context, deps Deps, cfg RunnerConfig, runID, repoID int64, repoName string) (RunResult, error) {
	if err := deps.validate(); err != nil {
		return RunResult{}, err
	}

	entityID := journalEntityID(runID, repoID)
	result := RunResult{RunID: runID, RepoID: repoID}

	for i, step := range cfg.Steps {
		// operationID pairs one step's Intent with its own Ack (journal's
		// dedupKey is (kind, operation_id) -- see runner.go's stepJournalPayload
		// doc comment); the index disambiguates a phase configured with more
		// than one command (e.g. two lint passes), which would otherwise share
		// one operation_id across distinct steps of the same StepKind.
		opID := fmt.Sprintf("%s-%d", step.Kind, i)
		deps.appendJournal(ctx, entityID, journal.KindIntent, opID, stepJournalPayload{Step: step.Kind})

		start := deps.Clock.Now()
		exec := deps.Exec.Run(ctx, ExecRequest{
			WorkDir: cfg.RepoRoot, Command: step.Command, Env: cfg.Env, Timeout: cfg.TimeoutPerStep,
		})
		sr := StepResult{
			Step: step, ExitCode: exec.ExitCode, Stdout: exec.Stdout, Stderr: exec.Stderr,
			TimedOut: exec.TimedOut, StartErr: exec.StartErr, Duration: deps.Clock.Now().Sub(start),
		}
		result.Steps = append(result.Steps, sr)

		outcome := "pass"
		if !sr.Passed() {
			outcome = "fail"
		}
		deps.appendJournal(ctx, entityID, journal.KindAck, opID, stepJournalPayload{Step: step.Kind, Result: outcome})

		if !sr.Passed() {
			result.FailedStep = step.Kind
			break
		}
	}

	if err := deps.writeResult(ctx, result, repoName); err != nil {
		return result, err
	}
	deps.publishCompleted(ctx, result)
	return result, nil
}

// appendJournal writes one journal entry, silently doing nothing when no
// JournalStore was injected -- journaling is observability, never a gate
// on whether the actual lint/test/build sequence runs (06 §5.20's
// fail-closed rule applies to unknown POLICY, not to an optional side
// channel). A journal WRITE failure is likewise not surfaced as a Run
// error for the same reason: the step already ran; losing its journal
// trail must never retroactively fail a passing gate.
func (d Deps) appendJournal(ctx context.Context, entityID string, kind journal.Kind, operationID string, payload stepJournalPayload) {
	if d.Journal == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = d.Journal.Append(ctx, entityID, kind, operationID, b)
}

// publishCompleted publishes EventKindRunCompleted, doing nothing when no
// Events bus was injected (same optional-side-effect reasoning as
// appendJournal).
func (d Deps) publishCompleted(ctx context.Context, result RunResult) {
	if d.Events == nil {
		return
	}
	payload, err := json.Marshal(runCompletedPayload{
		RunID: result.RunID, RepoID: result.RepoID, Passed: result.Passed(), FailedStep: result.FailedStep,
	})
	if err != nil {
		return
	}
	_, _ = d.Events.Publish(ctx, EventNamespace, EventKindRunCompleted, "ci-runner-local", payload)
}

// runCompletedPayload is EventKindRunCompleted's wire payload.
type runCompletedPayload struct {
	RunID      int64    `json:"run_id"`
	RepoID     int64    `json:"repo_id"`
	Passed     bool     `json:"passed"`
	FailedStep StepKind `json:"failed_step,omitempty"`
}

// writeResult persists result to the ci_results domain: one ci_run row
// (Upsert, unmodified T2 write path -- it upserts OVER the placeholder
// ReserveLocalRun already inserted), one ci_job row modeling the whole
// local run as a single job named "local" (a local run has no GitHub
// Actions job-fan-out concept to mirror), one ci_step row per executed
// RunnerStep, and the ci_run_source row marking source=local.
//
// The job id reuses the run id, which runner_ids.go allocates in the
// NEGATIVE integers -- so a local job id can never collide with a GitHub
// Actions job id (always positive) either, and the sign alone tells the
// two producers' rows apart in both tables.
func (d Deps) writeResult(ctx context.Context, result RunResult, repoName string) error {
	now := d.Clock.Now()
	run := Run{
		RunID: result.RunID, RepoID: result.RepoID, Name: repoName,
		HeadBranch: "", HeadSHA: "", Status: RunStatusCompleted,
		Conclusion: runConclusion(result), CreatedAt: now, UpdatedAt: now,
	}
	job := Job{
		JobID: result.RunID, RunID: result.RunID, Name: "local",
		Status: RunStatusCompleted, Conclusion: runConclusion(result),
		StartedAt: now, FinishedAt: now,
	}
	steps := make([]Step, 0, len(result.Steps))
	for i, sr := range result.Steps {
		steps = append(steps, Step{
			JobID: result.RunID, Number: i + 1, Name: string(sr.Step.Kind),
			Status: RunStatusCompleted, Conclusion: stepConclusion(sr),
			StartedAt: now, FinishedAt: now,
		})
	}

	if err := Upsert(ctx, d.DB, run, []Job{job}, steps); err != nil {
		return err
	}
	return UpsertRunSource(ctx, d.DB, result.RunID, result.RepoID, SourceLocal)
}

// runConclusion maps a RunResult's pass/fail to the canonical
// RunConclusion enum.
func runConclusion(result RunResult) RunConclusion {
	if result.Passed() {
		return ConclusionSuccess
	}
	return ConclusionFailure
}

// stepConclusion maps one StepResult to the canonical RunConclusion enum,
// distinguishing a timeout from an ordinary non-zero exit.
func stepConclusion(sr StepResult) RunConclusion {
	switch {
	case sr.Passed():
		return ConclusionSuccess
	case sr.TimedOut:
		return ConclusionTimedOut
	default:
		return ConclusionFailure
	}
}
