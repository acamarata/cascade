package jobs

// Purpose: the read side the P1-CI-01 streaming dispatcher's Resume needs
//
//	from the R-21.148 outbox: every unconfirmed row of ONE closed site.
//	outbox.go's own scan (unconfirmedOutboxRowsTx) is transaction-bound and
//	site-blind; this file adds the exported, site-scoped reader without a
//	second row shape or a second state vocabulary.
//
// Inputs: an open *sql.DB already migrated via ApplyOutboxSchema, and one
//
//	of the five closed OutboxSite values.
//
// Outputs: the rows left in state intent or effect for that site, oldest
//
//	first (ties broken by id so the order is total and deterministic).
//
// Constraints: an unknown site refuses with ErrInvalidOutboxSite and a nil
//
//	db refuses KindInvalidInput -- never an empty result that reads as
//	"nothing to resume". A confirmed or reconciled row is never returned.
//
// SPORT: jobs/scheduler-outbox/ADD (P1-CI-01).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// UnconfirmedOutbox returns every outbox row of site still in state intent
// or effect, oldest first. It is the exported, site-scoped counterpart of
// the resume scan outbox.go keeps private: a caller that owns one site
// (the CI dispatcher owns OutboxSiteCIDispatch) replays only its own rows.
func UnconfirmedOutbox(ctx context.Context, db *sql.DB, site OutboxSite) ([]OutboxRow, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: UnconfirmedOutbox requires a non-nil db")
	}
	if !site.Valid() {
		return nil, ErrInvalidOutboxSite
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, job_id, attempt_generation, site, idempotency_key, state, payload_hash, created_at, updated_at
		 FROM `+tableOutbox+` WHERE site = ? AND state IN (?, ?) ORDER BY created_at ASC, id ASC`,
		string(site), string(OutboxIntentState), string(OutboxEffectState))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: query unconfirmed rows by site")
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var siteText, state string
		if err := rows.Scan(&r.ID, &r.JobID, &r.AttemptGeneration, &siteText, &r.IdempotencyKey, &state, &r.PayloadHash, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: scan unconfirmed row by site")
		}
		r.Site, r.State = OutboxSite(siteText), OutboxState(state)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: iterate unconfirmed rows by site")
	}
	return out, nil
}
