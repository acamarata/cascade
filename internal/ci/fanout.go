package ci

// Purpose: route each terminal CI result once to the delegation that asked
//
//	for it (contract:ci-result-routing). RegisterCIFanout adds one
//	OnTerminal callback that projects a CIResultNotice from the ci_run and
//	ci_job rows of the event's run key and the job's delegation record,
//	then delivers it through the notify service.
//
// Routing: an origin principal gets a Class addressed item (TargetPrincipal
//
//	= OriginPrincipal, CorrelationID = DelegationID, never TargetSession);
//	a scope with no principal gets a Class scoped item; no origin at all
//	becomes a CI attention item. The last two are logged through Warn.
//	ClassGlobalCritical is never used for a CI result.
//
// Errors: a missing ci_run row is KindNotFound; a Deliver or Push failure
//
//	is returned by RouteToInbox unchanged. The OnTerminal callback has no
//	error return, so it reports both through Warn.
//
// SPORT: internal.ci.RegisterCIFanout/ADDED (P1-CI-06).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CIResultKind is the Source and Kind of every routed CI result.
const CIResultKind = "ci.result"

// The projected result statuses.
const (
	ResultAccepted  = "accepted"
	ResultRejected  = "rejected"
	ResultCancelled = "cancelled"
	ResultFailed    = "failed"
)

// CIResultNotice is one terminal CI result projected for its delegation
// origin. Status is accepted, rejected, cancelled or failed.
type CIResultNotice struct { //nolint:revive // contract-mandated exported name (P1-CI-06 outputs)
	JobID, DelegationID, RepoID, CommitSHA, Status, LogRef, OriginPrincipal, OriginSession, OriginScope string
	StartedAt, FinishedAt                                                                               time.Time
}

// DelegationOrigin resolves the delegation record a job was launched under
// and the scope it belongs to; found is false when the job has none.
type DelegationOrigin interface {
	OriginForJob(ctx context.Context, jobID string) (rec jobs.DelegationRecord, scope string, found bool, err error)
}

// Notifier is the delivery half of notify.Service (*notify.Notifier).
type Notifier interface {
	Deliver(ctx context.Context, n notify.Notification) error
}

// Warn logs a CI result that could not reach a principal. It is a variable
// so a test can observe the warning.
var Warn = func(msg string, args ...any) { slog.Default().Warn(msg, args...) }

// resultStatus maps a ci_run conclusion onto the notice status.
func resultStatus(conclusion string) string {
	switch RunConclusion(conclusion) {
	case ConclusionSuccess:
		return ResultAccepted
	case ConclusionFailure:
		return ResultRejected
	case ConclusionCancelled:
		return ResultCancelled
	case ConclusionSkipped, ConclusionTimedOut, ConclusionNone, ConclusionUnknown:
		return ResultFailed
	default:
		return ResultFailed
	}
}

// ProjectResult reads the ci_run row (RunID, RepoID) of ev and its ci_job
// row, looks up the job's delegation record, and returns the notice. A
// missing ci_run row is KindNotFound; a job without a delegation record
// projects with every origin field empty.
func ProjectResult(ctx context.Context, db *sql.DB, origins DelegationOrigin, ev CIResultEvent) (CIResultNotice, error) {
	if db == nil || origins == nil {
		return CIResultNotice{}, cascade.New(cascade.KindInvalidInput, "ci: ProjectResult requires a database and an origin source")
	}
	var sha, conclusion string
	var created, updated int64
	err := db.QueryRowContext(ctx, `SELECT head_sha, conclusion, created_at, updated_at FROM `+tableRun+
		` WHERE run_id = ? AND repo_id = ?`, ev.RunID, ev.RepoID).Scan(&sha, &conclusion, &created, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return CIResultNotice{}, cascade.Newf(cascade.KindNotFound, "ci: no ci_run row %d/%d", ev.RunID, ev.RepoID)
	case err != nil:
		return CIResultNotice{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: read the result run")
	}
	var jobStart, jobEnd sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT started_at, finished_at FROM `+tableJob+
		` WHERE run_id = ? ORDER BY job_id LIMIT 1`, ev.RunID).Scan(&jobStart, &jobEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CIResultNotice{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: read the result job")
	}
	if jobStart.Valid && jobStart.Int64 != 0 {
		created = jobStart.Int64
	}
	if jobEnd.Valid && jobEnd.Int64 != 0 {
		updated = jobEnd.Int64
	}
	notice := CIResultNotice{
		JobID: ev.JobID, RepoID: strconv.FormatInt(ev.RepoID, 10), CommitSHA: sha,
		Status: resultStatus(conclusion), LogRef: runLogRef(ev),
		StartedAt: time.UnixMilli(created).UTC(), FinishedAt: time.UnixMilli(updated).UTC(),
	}
	rec, scope, found, err := origins.OriginForJob(ctx, ev.JobID)
	if err != nil {
		return CIResultNotice{}, err
	}
	if found {
		notice.DelegationID, notice.OriginPrincipal, notice.OriginSession = rec.ID, rec.OriginPrincipal, rec.OriginSession
		notice.OriginScope = scope
	}
	return notice, nil
}

// runLogRef names the ci_run row a result's log hangs off.
func runLogRef(ev CIResultEvent) string {
	return tableRun + ":" + strconv.FormatInt(ev.RepoID, 10) + ":" + strconv.FormatInt(ev.RunID, 10)
}

// noticeRef is the stable identity of one notice: the job and its run.
func noticeRef(notice CIResultNotice) string {
	return CIResultKind + ":" + notice.JobID + ":" + notice.LogRef
}

// RouteToInbox delivers notice: addressed to its origin principal, scoped
// to its origin scope, or as a CI attention item through fallback. A
// Deliver or Push failure is returned.
func RouteToInbox(ctx context.Context, notice CIResultNotice, n Notifier, fallback AttentionPusher) error {
	payload, err := json.Marshal(notice)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "ci: encode the result notice")
	}
	base := notify.Notification{
		ID: noticeRef(notice), Source: CIResultKind, Kind: CIResultKind, Priority: notify.PriorityNormal,
		Payload: payload, CorrelationID: notice.DelegationID,
	}
	switch {
	case notice.OriginPrincipal != "":
		if n == nil {
			return cascade.New(cascade.KindInvalidInput, "ci: RouteToInbox has no notifier")
		}
		base.Class, base.TargetPrincipal, base.Visibility = notify.ClassAddressed, notice.OriginPrincipal, notify.VisibilityPrivate
		return n.Deliver(ctx, base)
	case notice.OriginScope != "":
		if n == nil {
			return cascade.New(cascade.KindInvalidInput, "ci: RouteToInbox has no notifier")
		}
		Warn("ci: result has no origin principal; delivering scoped", "job_id", notice.JobID, "scope", notice.OriginScope)
		base.Class, base.TargetScope, base.Visibility = notify.ClassScoped, notice.OriginScope, notify.VisibilityScoped
		return n.Deliver(ctx, base)
	}
	if fallback == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: RouteToInbox has no attention fallback")
	}
	Warn("ci: result has no delegation origin; filing an attention item", "job_id", notice.JobID)
	_, err = fallback.Push(ctx, supervision.AttentionItem{
		Kind: supervision.KindError, SourceRef: noticeRef(notice), ScopeRef: candidateScope(AttentionCandidate{}, nil),
	})
	return err
}

// RegisterCIFanout registers one OnTerminal callback on d that projects
// and routes each terminal result. The callback cannot return an error, so
// a projection or delivery failure is reported through Warn.
func RegisterCIFanout(d *Dispatcher, db *sql.DB, origins DelegationOrigin, n Notifier, fallback AttentionPusher) {
	d.OnTerminal(func(ev CIResultEvent) {
		ctx := context.Background()
		notice, err := ProjectResult(ctx, db, origins, ev)
		if err != nil {
			Warn("ci: could not project a terminal result", "job_id", ev.JobID, "run_id", ev.RunID, "error", err)
			return
		}
		if err := RouteToInbox(ctx, notice, n, fallback); err != nil {
			Warn("ci: could not route a terminal result", "job_id", ev.JobID, "run_id", ev.RunID, "error", err)
		}
	})
}
