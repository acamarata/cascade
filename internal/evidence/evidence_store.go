package evidence

// Purpose: Store's jobs_claim_evidence CRUD -- PutEvidence, EvidenceForClaim,
// EvidenceByID -- split from claim_store.go per the 300-line cap, following
// internal/jobs's store_job.go/store_exec.go split.
//
// Inputs: an open *sql.DB already migrated via ApplySchema (shared with
// claim_store.go through Store).
// Outputs: typed A-T7 errors.
// Constraints: data_class is write-once (ErrDataClassImmutable on a
// differing later write); PutEvidence refuses an evidence row for a
// nonexistent claim (ErrClaimNotFound) -- checked against the raw claim
// row, never through GetClaim, since the very first evidence row for a
// claim is written before that claim has any evidence yet.
//
// SPORT: evidence/evidence-record (ADD).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PutEvidence inserts a new evidence row, or updates an existing one.
// DataClass is immutable (R-21.94): an update whose DataClass differs
// from the stored row's is refused with ErrDataClassImmutable.
func (s *Store) PutEvidence(ctx context.Context, e Evidence) error {
	if e.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: evidence id is required")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if _, ok, err := s.rawClaim(ctx, e.ClaimID); err != nil {
		return err
	} else if !ok {
		return ErrClaimNotFound
	}
	existing, ok, err := s.rawEvidence(ctx, e.ID)
	if err != nil {
		return err
	}
	if ok && existing.DataClass != e.DataClass {
		return cascade.Wrapf(cascade.KindConflict, ErrDataClassImmutable,
			"evidence: evidence %q data_class is immutable: stored %q, refusing update to %q",
			e.ID, existing.DataClass, e.DataClass)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableClaimEvidence+` (evidence_id, claim_id, source_type, data_class, repository,
			"commit", path, locator, content_hash, captured_ref, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(evidence_id) DO UPDATE SET claim_id=excluded.claim_id, source_type=excluded.source_type,
			repository=excluded.repository, "commit"=excluded."commit", path=excluded.path,
			locator=excluded.locator, content_hash=excluded.content_hash, captured_ref=excluded.captured_ref,
			observed_at=excluded.observed_at`,
		e.ID, e.ClaimID, string(e.Source.Type), string(e.DataClass), e.Source.Repository, e.Source.Commit,
		e.Source.Path, e.Source.Locator.String(), e.ContentHash, e.CapturedRef, e.ObservedAt.Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "evidence: put evidence")
	}
	return nil
}

// EvidenceForClaim returns every evidence row for claimID, ordered by
// evidence id ascending -- the order Expand's deterministic paging walks.
func (s *Store) EvidenceForClaim(ctx context.Context, claimID string) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT evidence_id, claim_id, source_type, data_class, repository, "commit", path, locator,
			content_hash, captured_ref, observed_at
		 FROM `+tableClaimEvidence+` WHERE claim_id = ? ORDER BY evidence_id ASC`, claimID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: evidence for claim")
	}
	defer func() { _ = rows.Close() }()
	var out []Evidence
	for rows.Next() {
		e, err := scanEvidenceRow(rows)
		if err != nil {
			if _, ok := cascade.KindOf(err); ok {
				return nil, err
			}
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: scan evidence row")
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: iterate evidence for claim")
	}
	return out, nil
}

// EvidenceByID reads one evidence row by id.
func (s *Store) EvidenceByID(ctx context.Context, id string) (Evidence, error) {
	e, ok, err := s.rawEvidence(ctx, id)
	if err != nil {
		return Evidence{}, err
	}
	if !ok {
		return Evidence{}, ErrEvidenceNotFound
	}
	return e, nil
}

func (s *Store) rawEvidence(ctx context.Context, id string) (Evidence, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT evidence_id, claim_id, source_type, data_class, repository, "commit", path, locator,
			content_hash, captured_ref, observed_at
		 FROM `+tableClaimEvidence+` WHERE evidence_id = ?`, id)
	e, err := scanEvidenceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, false, nil
	}
	if err != nil {
		if _, ok := cascade.KindOf(err); ok {
			// scanEvidenceRow's own typed decode error -- pass through
			// unchanged rather than reclassifying it as a storage failure.
			return Evidence{}, false, err
		}
		return Evidence{}, false, cascade.Wrap(cascade.KindUnavailable, err, "evidence: get evidence")
	}
	return e, true, nil
}

func scanEvidenceRow(row rowScanner) (Evidence, error) {
	var e Evidence
	var sourceType, dataClass, locatorStr string
	var observedAt int64
	err := row.Scan(&e.ID, &e.ClaimID, &sourceType, &dataClass, &e.Source.Repository, &e.Source.Commit,
		&e.Source.Path, &locatorStr, &e.ContentHash, &e.CapturedRef, &observedAt)
	if err != nil {
		return Evidence{}, err
	}
	if e.Source.Type, err = DecodeSourceType(sourceType); err != nil {
		return Evidence{}, err
	}
	if e.DataClass, err = DecodeDataClass(dataClass); err != nil {
		return Evidence{}, err
	}
	if e.Source.Locator, err = ParseLocator(locatorStr); err != nil {
		return Evidence{}, err
	}
	e.ObservedAt = unixToTime(observedAt)
	return e, nil
}
