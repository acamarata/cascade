// Purpose: parse and normalize GitHub Actions REST API responses (workflow
//
//	runs and their jobs/steps) into the ci_results canonical schema (Run,
//	Job, Step), per P1-E25-W5-S51-T2 and R-16.75.
//
// Inputs: raw JSON bytes for one "list workflow runs" page and one
//
//	"list jobs for a run" page, exactly as api.github.com returns them
//	(see internal/ci/testdata/fixtures/, provenance-stamped, Art.2).
//
// Outputs: []Run / []Job / []Step, or a taxonomy error on malformed JSON.
//
//	status/conclusion values GitHub has not documented, or has added since
//	this ticket, normalize to "unknown" rather than being refused -- fail-
//	closed per 06 §5.20: an unrecognized value is handled, not rejected.
//
// Constraints: pure decode/normalize, no I/O, no *sql.DB -- poll.go and
//
//	domain.go own persistence. No bare time.Now (forbidigo): timestamps
//	come from the wire payload's own RFC3339 strings.
//
// SPORT: internal.ci.normalizeActionsRuns/ADDED,
//
//	internal.ci.normalizeActionsJobs/ADDED (P1-E25-W5-S51-T2).

package ci

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RunStatus is the ci_results canonical run-status vocabulary. GitHub's own
// enum ("queued", "in_progress", "completed") is not closed against this
// build's knowledge of it, so "unknown" is a real, storable member rather
// than an error -- see Art.1 fail-closed framing in 06 §5.20.
type RunStatus string

// The canonical RunStatus vocabulary.
const (
	RunStatusQueued     RunStatus = "queued"
	RunStatusInProgress RunStatus = "in_progress"
	RunStatusCompleted  RunStatus = "completed"
	RunStatusUnknown    RunStatus = "unknown"
)

// RunConclusion is the ci_results canonical conclusion vocabulary,
// covering both run-level and job/step-level conclusion fields. This
// ticket's own contract (full_desc) states the canonical set explicitly:
// "success | failure | cancelled | skipped | timed_out". GitHub's REST
// API additionally documents action_required, neutral and stale at
// various endpoints, but those are NOT members of this ticket's canonical
// enum -- they normalize to Unknown exactly like a value GitHub adds
// tomorrow, per 06 §5.20's fail-closed rule. The capture swept this
// repo's full history and found none of the three in the wild (testdata/
// README.md), so treating them as unknown-until-a-future-ticket-widens-
// the-enum costs nothing observable today.
type RunConclusion string

// The canonical RunConclusion vocabulary (this ticket's contract, not
// GitHub's full documented set -- see the type doc comment above).
const (
	ConclusionSuccess   RunConclusion = "success"
	ConclusionFailure   RunConclusion = "failure"
	ConclusionCancelled RunConclusion = "cancelled"
	ConclusionSkipped   RunConclusion = "skipped"
	ConclusionTimedOut  RunConclusion = "timed_out"
	ConclusionNone      RunConclusion = "" // still running / no conclusion yet
	ConclusionUnknown   RunConclusion = "unknown"
)

// knownRunStatuses and knownConclusions are the closed allow-lists
// normalizeRunStatus/normalizeConclusion check membership against. Anything
// not in the set maps to the Unknown member -- fail-closed, never refused.
var knownRunStatuses = map[string]RunStatus{
	"queued":      RunStatusQueued,
	"in_progress": RunStatusInProgress,
	"completed":   RunStatusCompleted,
}

var knownConclusions = map[string]RunConclusion{
	"":          ConclusionNone,
	"success":   ConclusionSuccess,
	"failure":   ConclusionFailure,
	"cancelled": ConclusionCancelled,
	"skipped":   ConclusionSkipped,
	"timed_out": ConclusionTimedOut,
}

// normalizeRunStatus maps a raw wire status string to the canonical
// RunStatus. An unrecognized value normalizes to RunStatusUnknown rather
// than being refused (06 §5.20).
func normalizeRunStatus(raw string) RunStatus {
	if v, ok := knownRunStatuses[raw]; ok {
		return v
	}
	return RunStatusUnknown
}

// normalizeConclusion maps a raw wire conclusion string to the canonical
// RunConclusion. An unrecognized value normalizes to ConclusionUnknown.
func normalizeConclusion(raw string) RunConclusion {
	if v, ok := knownConclusions[raw]; ok {
		return v
	}
	return ConclusionUnknown
}

// Run is one normalized GitHub Actions workflow run: the ci_run canonical
// record.
type Run struct {
	RunID      int64
	RepoID     int64
	Name       string
	HeadBranch string
	HeadSHA    string
	Status     RunStatus
	Conclusion RunConclusion
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Job is one normalized job within a run: the ci_job canonical record.
type Job struct {
	JobID      int64
	RunID      int64
	Name       string
	Status     RunStatus
	Conclusion RunConclusion
	StartedAt  time.Time
	FinishedAt time.Time
}

// Step is one normalized step within a job: the ci_step canonical record.
type Step struct {
	JobID      int64
	Number     int
	Name       string
	Status     RunStatus
	Conclusion RunConclusion
	StartedAt  time.Time
	FinishedAt time.Time
}

// wireRun/wireJob/wireStep mirror only the fields this ticket needs from
// api.github.com's actual response shape (testdata/fixtures/*.json), never
// a full re-declaration of GitHub's schema.
type wireRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type wireRunsPage struct {
	TotalCount   int       `json:"total_count"`
	WorkflowRuns []wireRun `json:"workflow_runs"`
}

type wireStep struct {
	Number      int    `json:"number"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

type wireJob struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   string     `json:"started_at"`
	CompletedAt string     `json:"completed_at"`
	Steps       []wireStep `json:"steps"`
}

type wireJobsPage struct {
	TotalCount int       `json:"total_count"`
	Jobs       []wireJob `json:"jobs"`
}

// parseWireTime parses an RFC3339 wire timestamp. An empty or unparseable
// value returns the zero time.Time rather than an error -- a run that has
// not yet started legitimately has no started_at, and Art.1's fail-closed
// framing applies to the enum fields, not to an absent timestamp.
func parseWireTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// normalizeActionsRuns decodes one "list workflow runs" page and returns
// its normalized Run records, tagged with repoID (the caller's own
// (owner,repo) identity -- the wire payload carries no repo id of its
// own at this endpoint).
func normalizeActionsRuns(body []byte, repoID int64) ([]Run, error) {
	runs, _, err := normalizeActionsRunsPage(body, repoID)
	return runs, err
}

// normalizeActionsRunsPage is normalizeActionsRuns plus the page's own
// total_count, which poll.go's pagination loop needs to know when to
// stop requesting further pages.
func normalizeActionsRunsPage(body []byte, repoID int64) ([]Run, int, error) {
	var page wireRunsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, cascade.Wrap(cascade.KindInvalidInput, err, "ci: decoding a workflow-runs page")
	}
	runs := make([]Run, 0, len(page.WorkflowRuns))
	for _, wr := range page.WorkflowRuns {
		runs = append(runs, Run{
			RunID:      wr.ID,
			RepoID:     repoID,
			Name:       wr.Name,
			HeadBranch: wr.HeadBranch,
			HeadSHA:    wr.HeadSHA,
			Status:     normalizeRunStatus(wr.Status),
			Conclusion: normalizeConclusion(wr.Conclusion),
			CreatedAt:  parseWireTime(wr.CreatedAt),
			UpdatedAt:  parseWireTime(wr.UpdatedAt),
		})
	}
	return runs, page.TotalCount, nil
}

// normalizeActionsJobs decodes one "list jobs for a run" page and returns
// its normalized Job records plus every Job's normalized Step records.
func normalizeActionsJobs(body []byte) ([]Job, []Step, error) {
	var page wireJobsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInvalidInput, err, "ci: decoding a jobs page")
	}
	jobs := make([]Job, 0, len(page.Jobs))
	var steps []Step
	for _, wj := range page.Jobs {
		jobs = append(jobs, Job{
			JobID:      wj.ID,
			RunID:      wj.RunID,
			Name:       wj.Name,
			Status:     normalizeRunStatus(wj.Status),
			Conclusion: normalizeConclusion(wj.Conclusion),
			StartedAt:  parseWireTime(wj.StartedAt),
			FinishedAt: parseWireTime(wj.CompletedAt),
		})
		for _, ws := range wj.Steps {
			steps = append(steps, Step{
				JobID:      wj.ID,
				Number:     ws.Number,
				Name:       ws.Name,
				Status:     normalizeRunStatus(ws.Status),
				Conclusion: normalizeConclusion(ws.Conclusion),
				StartedAt:  parseWireTime(ws.StartedAt),
				FinishedAt: parseWireTime(ws.CompletedAt),
			})
		}
	}
	return jobs, steps, nil
}

// normalizeActionsRun is FuzzNormalizeActionsRun's target: it drives the
// same decode path as normalizeActionsRuns but over exactly one page,
// returning only the error so the fuzz harness has one narrow property to
// check (never panics, never returns a partially-populated slice
// alongside a non-nil error).
func normalizeActionsRun(body []byte) ([]Run, error) {
	return normalizeActionsRuns(body, 0)
}
