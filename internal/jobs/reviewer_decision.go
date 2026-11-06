package jobs

// Purpose: durable reviewer decision rows on the jobs Store. Each dispatch
// or completion review decision is recorded once under a content-derived
// ID; the review EvidenceRecord points at it through ReviewEvidenceRef.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The closed review phase vocabulary.
const (
	ReviewPhaseDispatch   = "dispatch"
	ReviewPhaseCompletion = "completion"
)

// The closed review outcome vocabulary.
const (
	ReviewOutcomeAssigned         = "assigned"
	ReviewOutcomeDegraded         = "degraded"
	ReviewOutcomeHeld             = "held"
	ReviewOutcomeDeniedMutated    = "denied-mutated-worktree"
	ReviewOutcomeDeniedUnreadable = "denied-unreadable"
)

// ErrInvalidReviewerDecision refuses a decision outside the closed sets.
var ErrInvalidReviewerDecision = cascade.New(cascade.KindInvalidInput, "jobs: invalid reviewer decision")

// ReviewerDecision is one recorded review decision for a job.
type ReviewerDecision struct {
	ID, JobID, Phase, RiskClass, Outcome string
	Authors                              []FamilyRef
	Reviewer                             FamilyRef
	FamilyDistinct, Degraded             bool
	DecidedAt                            time.Time
}

// reviewerDecisionCanon is the canonical JSON shape the ID hashes: every
// field but ID, DecidedAt as UTC unix milliseconds, Authors in given order.
type reviewerDecisionCanon struct {
	JobID          string      `json:"job_id"`
	Phase          string      `json:"phase"`
	RiskClass      string      `json:"risk_class"`
	Outcome        string      `json:"outcome"`
	Authors        []FamilyRef `json:"authors"`
	Reviewer       FamilyRef   `json:"reviewer"`
	FamilyDistinct bool        `json:"family_distinct"`
	Degraded       bool        `json:"degraded"`
	DecidedAtMs    int64       `json:"decided_at_ms"`
}

// RecordReviewerDecision validates d, derives its ID and inserts it once.
// A repeat of the same decision leaves one row and returns the same ID.
func (s *Store) RecordReviewerDecision(ctx context.Context, d ReviewerDecision) (ReviewerDecision, error) {
	if err := validateReviewerDecision(d); err != nil {
		return ReviewerDecision{}, err
	}
	if d.Authors == nil {
		d.Authors = []FamilyRef{}
	}
	d.DecidedAt = time.UnixMilli(d.DecidedAt.UTC().UnixMilli()).UTC()
	authors, err := json.Marshal(d.Authors)
	if err != nil {
		return ReviewerDecision{}, cascade.Wrap(cascade.KindInternal, err, "jobs: encode reviewer authors")
	}
	id, err := reviewerDecisionID(d)
	if err != nil {
		return ReviewerDecision{}, err
	}
	d.ID = id
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+tableReviewerDecision+` (id, job_id, phase, risk_class, outcome,
		authors, reviewer_family, reviewer_resolved, family_distinct, degraded, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		d.ID, d.JobID, d.Phase, d.RiskClass, d.Outcome, string(authors), d.Reviewer.Family,
		boolInt(d.Reviewer.Resolved), boolInt(d.FamilyDistinct), boolInt(d.Degraded), d.DecidedAt.UnixMilli())
	if err != nil {
		return ReviewerDecision{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: record reviewer decision")
	}
	return d, nil
}

// ReviewerDecisions lists jobID's decisions by decided_at, then id.
func (s *Store) ReviewerDecisions(ctx context.Context, jobID string) ([]ReviewerDecision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, phase, risk_class, outcome, authors, reviewer_family,
		reviewer_resolved, family_distinct, degraded, decided_at FROM `+tableReviewerDecision+`
		WHERE job_id = ? ORDER BY decided_at, id`, jobID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list reviewer decisions")
	}
	defer func() { _ = rows.Close() }()
	out := []ReviewerDecision{}
	for rows.Next() {
		d, err := scanReviewerDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list reviewer decisions")
	}
	return out, nil
}

// ReviewEvidenceRef is the review EvidenceRecord.ArtifactRef for d.
func ReviewEvidenceRef(d ReviewerDecision) string {
	return "reviewer-decision:" + d.ID
}

func scanReviewerDecision(r rowScanner) (ReviewerDecision, error) {
	var d ReviewerDecision
	var authors string
	var resolved, distinct, degraded int
	var decidedMs int64
	if err := r.Scan(&d.ID, &d.JobID, &d.Phase, &d.RiskClass, &d.Outcome, &authors, &d.Reviewer.Family,
		&resolved, &distinct, &degraded, &decidedMs); err != nil {
		return ReviewerDecision{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan reviewer decision")
	}
	if err := json.Unmarshal([]byte(authors), &d.Authors); err != nil {
		return ReviewerDecision{}, cascade.Wrap(cascade.KindInternal, err, "jobs: decode reviewer authors")
	}
	d.Reviewer.Resolved = resolved != 0
	d.FamilyDistinct = distinct != 0
	d.Degraded = degraded != 0
	d.DecidedAt = time.UnixMilli(decidedMs).UTC()
	return d, nil
}

func validateReviewerDecision(d ReviewerDecision) error {
	switch {
	case d.JobID == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidReviewerDecision, "jobs: reviewer decision has no job id")
	case d.Phase != ReviewPhaseDispatch && d.Phase != ReviewPhaseCompletion:
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidReviewerDecision, "jobs: reviewer decision phase %q", d.Phase)
	case !validReviewOutcome(d.Outcome):
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidReviewerDecision, "jobs: reviewer decision outcome %q", d.Outcome)
	case !validReviewRiskClass(d.RiskClass):
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidReviewerDecision, "jobs: reviewer decision risk class %q", d.RiskClass)
	case d.DecidedAt.IsZero():
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidReviewerDecision, "jobs: reviewer decision has no decided_at")
	}
	return nil
}

func validReviewOutcome(o string) bool {
	switch o {
	case ReviewOutcomeAssigned, ReviewOutcomeDegraded, ReviewOutcomeHeld,
		ReviewOutcomeDeniedMutated, ReviewOutcomeDeniedUnreadable:
		return true
	}
	return false
}

func validReviewRiskClass(c string) bool {
	switch RiskClass(c) {
	case RiskClassLow, RiskClassNormal, RiskClassHigh, RiskClassCritical:
		return true
	}
	return false
}

func reviewerDecisionID(d ReviewerDecision) (string, error) {
	b, err := json.Marshal(reviewerDecisionCanon{
		JobID: d.JobID, Phase: d.Phase, RiskClass: d.RiskClass, Outcome: d.Outcome,
		Authors: d.Authors, Reviewer: d.Reviewer, FamilyDistinct: d.FamilyDistinct,
		Degraded: d.Degraded, DecidedAtMs: d.DecidedAt.UTC().UnixMilli(),
	})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "jobs: encode reviewer decision")
	}
	sum := sha256.Sum256(b)
	return "revdec:" + hex.EncodeToString(sum[:]), nil
}
