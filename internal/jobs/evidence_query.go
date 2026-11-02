package jobs

// Purpose: EvidenceLedger.QueryAll, RedactForCeiling, the jobs.evidence
//
//	namespace constants and the EvidenceChanged event payload (P1-PBD-06).
//	QueryAll returns a job's rows in append order with their effective
//	data class; RedactForCeiling elides sensitive fields above the fixed
//	EvidenceReadCeiling without dropping rows.
//
// SPORT: jobs/evidence-ledger-read/ADD (P1-PBD-06).

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EvidenceNamespace is the events bus namespace for evidence changes.
const EvidenceNamespace = "jobs.evidence"

// EventEvidenceChanged is the event kind published after a successful Append.
const EventEvidenceChanged events.EventKind = "jobs.evidence.changed"

// EvidenceReadCeiling is the fixed data-class ceiling for read paths.
// Records above this class are returned redacted; records at or below
// pass unchanged. The ceiling is SensitivityInternal per P1-SEC-34.
const EvidenceReadCeiling = provider.SensitivityInternal

// EvidenceChanged is the payload published on EvidenceNamespace after
// a successful Append commits.
type EvidenceChanged struct {
	JobID     string       `json:"job_id"`
	Seq       int64        `json:"seq"`
	Kind      EvidenceKind `json:"kind"`
	Outcome   Outcome      `json:"outcome"`
	DataClass string       `json:"data_class"`
}

// WithBus attaches an events bus to the ledger for publish-after-commit.
// It may be called once before serve; nil means no publish.
func (l *EvidenceLedger) WithBus(bus *events.Bus) *EvidenceLedger {
	l.bus = bus
	return l
}

// QueryAll returns jobID's evidence rows in ascending seq order, each
// carrying its effective data class. An empty ledger returns
// ErrNoEvidence (identity + message); a query error is returned as-is,
// never an empty slice.
func (l *EvidenceLedger) QueryAll(ctx context.Context, jobID string) ([]EvidenceRecord, error) {
	if l == nil || l.store == nil || l.store.db == nil {
		return nil, cascade.New(cascade.KindUnavailable, "jobs: QueryAll requires an open ledger")
	}
	rows, err := l.store.db.QueryContext(ctx, `SELECT e.seq, e.kind, e.producer_capability, e.attempt_id,
		e.checkpoint_id, e.tree_hash, e.attestor_identity, e.prev_hash, e.started_at, e.ended_at,
		e.outcome, e.result_hash, e.idempotency_key, e.artifact_ref, e.created_at, c.data_class
		FROM `+tableEvidence+` e LEFT JOIN `+tableEvidenceClass+` c ON e.job_id = c.job_id AND e.seq = c.seq
		WHERE e.job_id = ? ORDER BY e.seq ASC`, jobID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: QueryAll")
	}
	defer func() { _ = rows.Close() }()

	var out []EvidenceRecord
	for rows.Next() {
		rec, err := scanEvidenceRowWithClass(rows, jobID)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: QueryAll iterate")
	}
	// Close before GetJob: the domain store may have one connection.
	if err := rows.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: QueryAll close")
	}
	for i := range out {
		if out[i].DataClass == nil {
			class := l.jobEvidenceClass(ctx, jobID)
			out[i].DataClass = &class
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEvidence
	}
	return out, nil
}

// RedactForCeiling returns a copy of rec with sensitive fields emptied
// when rec's data class is more restrictive than ceiling or invalid.
// The second return value is true when redaction was applied. Fields
// cleared: ArtifactRef, ResultHash, AttestorIdentity, IdempotencyKey.
func RedactForCeiling(rec EvidenceRecord, ceiling provider.SensitivityTier) (EvidenceRecord, bool) {
	tier := provider.TierOrLocalOnly(rec.DataClass)
	if !tier.MoreRestrictiveThan(ceiling) {
		return rec, false
	}
	out := rec
	out.ArtifactRef = ""
	out.ResultHash = ""
	out.AttestorIdentity = ""
	out.IdempotencyKey = ""
	return out, true
}

// tierOrLocalOnly parses a stored data-class word into a SensitivityTier,
// returning SensitivityLocalOnly for empty or unknown values (fail closed).
func tierOrLocalOnly(word string) provider.SensitivityTier {
	if word == "" {
		return provider.SensitivityLocalOnly
	}
	tier, err := provider.ParseDataClassName(word)
	if err != nil {
		return provider.SensitivityLocalOnly
	}
	return tier
}

// scanEvidenceRowWithClass scans one joined evidence+class row.
func scanEvidenceRowWithClass(rows *sql.Rows, jobID string) (EvidenceRecord, error) {
	var rec EvidenceRecord
	var kind, outcome, dataClass sql.NullString
	var prev []byte
	var started, ended, created int64
	err := rows.Scan(&rec.Seq, &kind, &rec.ProducerCapability, &rec.AttemptID,
		&rec.CheckpointID, &rec.TreeHash, &rec.AttestorIdentity, &prev,
		&started, &ended, &outcome, &rec.ResultHash, &rec.IdempotencyKey,
		&rec.ArtifactRef, &created, &dataClass)
	if err != nil {
		return EvidenceRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: QueryAll scan")
	}
	rec.JobID = jobID
	if kind.Valid {
		rec.Kind = EvidenceKind(kind.String)
	}
	if outcome.Valid {
		rec.Outcome = Outcome(outcome.String)
	}
	rec.PrevHash = prev
	rec.StartedAt = unixToTime(started)
	rec.EndedAt = unixToTime(ended)
	rec.CreatedAt = unixToTime(created)
	if dataClass.Valid {
		class := tierOrLocalOnly(dataClass.String)
		rec.DataClass = &class
	}
	return rec, nil
}

func (l *EvidenceLedger) jobEvidenceClass(ctx context.Context, jobID string) provider.SensitivityTier {
	job, found, err := l.store.GetJob(ctx, jobID)
	if err != nil || !found {
		return provider.SensitivityLocalOnly
	}
	return provider.TierOrLocalOnly(job.DataClass)
}

func (l *EvidenceLedger) effectiveEvidenceClass(ctx context.Context, rec EvidenceRecord) provider.SensitivityTier {
	job := l.jobEvidenceClass(ctx, rec.JobID)
	if rec.DataClass == nil {
		return job
	}
	return provider.JoinSensitivity(job, provider.TierOrLocalOnly(rec.DataClass))
}

func (l *EvidenceLedger) publishEvidenceChanged(ctx context.Context, rec EvidenceRecord) {
	if l.bus == nil {
		return
	}
	class, _ := provider.DataClassName(provider.TierOrLocalOnly(rec.DataClass))
	payload, err := json.Marshal(EvidenceChanged{rec.JobID, rec.Seq, rec.Kind, rec.Outcome, class})
	if err == nil {
		_, _ = l.bus.Publish(context.WithoutCancel(ctx), EvidenceNamespace, EventEvidenceChanged, EvidenceNamespace, payload)
	}
}
