// Purpose: SchedulerModeStore -- the per-project scheduler_mode record
//   (R-21.22, storage domain `policy`) and its Set/Resolve/Clear API, plus
//   the R-21.118 incident-renewal cap and its HUMAN_APPROVAL_REQUIRED
//   journal.
//
// Inputs: a *sql.DB already migrated via ApplyMigrationSchema, and an
//   injected Clock (Art.7.3 -- no bare time.Now).
// Outputs: ModeState rows, or a pkg/cascade taxonomy error.
// Constraints: Resolve is read-only -- an expired incident row is never
//   mutated by a read, only computed as a virtual lifecycle-derived
//   result; the physical row (and its renewal counters) is changed only
//   by Set/Clear. project_id is treated as an opaque caller-supplied
//   string (the E/S-08.T4 SessionScope's `project` field) -- this file
//   adds no project resolver of its own.
//
// SPORT: fleet/economics/mode-store/ADD (P1-E41-W9-S79-T2).

package economics

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ModeSource distinguishes an explicitly-Set mode from one derived from
// the caller's lifecycle stage.
type ModeSource string

const (
	// ModeSourceExplicit means the row was written by a Set call and is
	// still live (not yet expired).
	ModeSourceExplicit ModeSource = "explicit"
	// ModeSourceLifecycle means no live explicit row exists for the
	// project; the mode was derived from ModeForLifecycleStage.
	ModeSourceLifecycle ModeSource = "lifecycle"
)

// ModeState is one project's resolved scheduler mode.
type ModeState struct {
	ProjectID string
	Mode      Mode
	Source    ModeSource
	// SetAt is the instant an explicit row was last written; zero for a
	// lifecycle-derived result.
	SetAt time.Time
	// ExpiresAt is the instant an explicit incident row auto-reverts;
	// zero when the state carries no expiry (every non-incident explicit
	// Set, and every lifecycle-derived result).
	ExpiresAt time.Time
}

// JournalEntry is one row of policy_scheduler_mode_journal.
type JournalEntry struct {
	ProjectID  string
	Event      string
	ApprovalID string
	RecordedAt time.Time
}

// EventHumanApprovalRequired is the journal event this store records when
// the R-21.118 incident-renewal cap refuses a fourth consecutive Set.
const EventHumanApprovalRequired = "HUMAN_APPROVAL_REQUIRED"

// ApprovalIDIncidentRenewalCap names the I/S-18.T3 approval an operator
// must grant before a further incident renewal is accepted once the
// R-21.118 cap is hit.
const ApprovalIDIncidentRenewalCap = "I-S18-T3-incident-renewal-cap"

// incidentDefaultTTL is R-21.32's incident mode default TTL.
const incidentDefaultTTL = 4 * time.Hour

// incidentRenewalCap is R-21.118's limit: at most this many consecutive
// incident Set calls succeed per project per week; the next one is
// refused.
const incidentRenewalCap = 3

// incidentRenewalWindow is the R-21.118 rolling window a renewal count is
// scoped to.
const incidentRenewalWindow = 7 * 24 * time.Hour

// ErrModeTTLNotApplicable is returned by Set when ttl is non-zero and m
// is not ModeIncident (R-21.32: only incident carries a TTL).
var ErrModeTTLNotApplicable = cascade.New(cascade.KindInvalidInput, "economics: ttl is only applicable to incident mode")

// ErrIncidentRenewalCap is returned by Set when a project's fourth
// consecutive incident Set inside one week is attempted (R-21.118).
var ErrIncidentRenewalCap = cascade.New(cascade.KindConflict, "economics: incident mode renewal cap reached for this project this week")

// SchedulerModeStore is the CRUD surface over policy_scheduler_mode and
// policy_scheduler_mode_journal. The zero value is not usable; construct
// with NewSchedulerModeStore.
type SchedulerModeStore struct {
	db    *sql.DB
	clock Clock
}

// NewSchedulerModeStore returns a SchedulerModeStore persisting through
// db (already migrated via ApplyMigrationSchema) and reading time.Now
// only through clock.
func NewSchedulerModeStore(db *sql.DB, clock Clock) *SchedulerModeStore {
	return &SchedulerModeStore{db: db, clock: clock}
}

// modeRow is the raw persisted shape of one policy_scheduler_mode row.
type modeRow struct {
	mode               string
	source             string
	setAt              int64
	expiresAt          sql.NullInt64
	incidentRenewals   int64
	renewalWindowStart sql.NullInt64
}

// selectModeRow reads projectID's row, or (modeRow{}, false, nil) when
// none exists.
func (s *SchedulerModeStore) selectModeRow(ctx context.Context, projectID string) (modeRow, bool, error) {
	var r modeRow
	err := s.db.QueryRowContext(ctx, `
SELECT mode, source, set_at, expires_at, incident_renewals, renewal_window_start
FROM `+tableSchedulerMode+` WHERE project_id = ?`, projectID).Scan(
		&r.mode, &r.source, &r.setAt, &r.expiresAt, &r.incidentRenewals, &r.renewalWindowStart)
	if errors.Is(err, sql.ErrNoRows) {
		return modeRow{}, false, nil
	}
	if err != nil {
		return modeRow{}, false, cascade.Wrap(cascade.KindUnavailable, err, "economics: select scheduler_mode")
	}
	return r, true, nil
}

// Set writes projectID's explicit scheduler mode. ttl is defined only for
// ModeIncident: a non-zero ttl with any other mode returns
// ErrModeTTLNotApplicable, and a zero ttl with ModeIncident applies the
// 4h R-21.32 default. A non-incident Set resets the R-21.118 renewal
// counters to zero. An incident Set beyond the R-21.118 cap writes
// nothing and records a HUMAN_APPROVAL_REQUIRED journal entry instead.
func (s *SchedulerModeStore) Set(ctx context.Context, projectID string, m Mode, ttl time.Duration) (ModeState, error) {
	if projectID == "" {
		return ModeState{}, cascade.New(cascade.KindInvalidInput, "economics: Set requires a non-empty project_id")
	}
	if !m.Valid() {
		return ModeState{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
	}
	if ttl != 0 && m != ModeIncident {
		return ModeState{}, ErrModeTTLNotApplicable
	}
	now := s.clock.Now()
	if m != ModeIncident {
		return s.setNonIncident(ctx, projectID, m, now)
	}
	return s.setIncident(ctx, projectID, ttl, now)
}

// setNonIncident writes m (any mode but incident) and resets the
// R-21.118 renewal counters.
func (s *SchedulerModeStore) setNonIncident(ctx context.Context, projectID string, m Mode, now time.Time) (ModeState, error) {
	if err := s.upsert(ctx, projectID, string(m), string(ModeSourceExplicit), now.Unix(), sql.NullInt64{}, 0, sql.NullInt64{}); err != nil {
		return ModeState{}, err
	}
	return ModeState{ProjectID: projectID, Mode: m, Source: ModeSourceExplicit, SetAt: now}, nil
}

// setIncident applies R-21.118's renewal-cap logic before writing.
func (s *SchedulerModeStore) setIncident(ctx context.Context, projectID string, ttl time.Duration, now time.Time) (ModeState, error) {
	if ttl == 0 {
		ttl = incidentDefaultTTL
	}
	existing, ok, err := s.selectModeRow(ctx, projectID)
	if err != nil {
		return ModeState{}, err
	}
	renewals := int64(0)
	windowStart := now
	if ok && existing.renewalWindowStart.Valid {
		elapsed := now.Sub(time.Unix(existing.renewalWindowStart.Int64, 0))
		if elapsed < incidentRenewalWindow {
			renewals = existing.incidentRenewals
			windowStart = time.Unix(existing.renewalWindowStart.Int64, 0)
		}
	}
	if renewals >= incidentRenewalCap {
		if err := s.recordApprovalJournal(ctx, projectID, now); err != nil {
			return ModeState{}, err
		}
		return ModeState{}, ErrIncidentRenewalCap
	}
	renewals++
	expiresAt := now.Add(ttl)
	if err := s.upsert(ctx, projectID, string(ModeIncident), string(ModeSourceExplicit), now.Unix(),
		sql.NullInt64{Int64: expiresAt.Unix(), Valid: true}, renewals,
		sql.NullInt64{Int64: windowStart.Unix(), Valid: true}); err != nil {
		return ModeState{}, err
	}
	return ModeState{ProjectID: projectID, Mode: ModeIncident, Source: ModeSourceExplicit, SetAt: now, ExpiresAt: expiresAt}, nil
}

// upsert writes one policy_scheduler_mode row, replacing any existing row
// for projectID.
func (s *SchedulerModeStore) upsert(ctx context.Context, projectID, mode, source string, setAt int64, expiresAt sql.NullInt64, incidentRenewals int64, windowStart sql.NullInt64) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO `+tableSchedulerMode+` (project_id, mode, source, set_at, expires_at, incident_renewals, renewal_window_start)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id) DO UPDATE SET
  mode=excluded.mode, source=excluded.source, set_at=excluded.set_at,
  expires_at=excluded.expires_at, incident_renewals=excluded.incident_renewals,
  renewal_window_start=excluded.renewal_window_start`,
		projectID, mode, source, setAt, expiresAt, incidentRenewals, windowStart)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: upsert scheduler_mode")
	}
	return nil
}

// recordApprovalJournal appends a HUMAN_APPROVAL_REQUIRED row naming the
// I/S-18.T3 approval a further incident renewal requires.
func (s *SchedulerModeStore) recordApprovalJournal(ctx context.Context, projectID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO `+tableSchedulerModeJournal+` (project_id, event, approval_id, recorded_at)
VALUES (?, ?, ?, ?)`, projectID, EventHumanApprovalRequired, ApprovalIDIncidentRenewalCap, now.Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: record scheduler_mode journal")
	}
	return nil
}

// IncidentJournal returns every journal entry recorded for projectID, in
// insertion order -- test/operator visibility into the R-21.118 cap.
func (s *SchedulerModeStore) IncidentJournal(ctx context.Context, projectID string) ([]JournalEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, event, approval_id, recorded_at FROM `+tableSchedulerModeJournal+`
WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: select scheduler_mode journal")
	}
	defer func() { _ = rows.Close() }()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var recordedAt int64
		if err := rows.Scan(&e.ProjectID, &e.Event, &e.ApprovalID, &recordedAt); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: scan scheduler_mode journal row")
		}
		e.RecordedAt = time.Unix(recordedAt, 0)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: iterate scheduler_mode journal")
	}
	return out, nil
}

// Resolve returns projectID's live explicit mode, or the
// ModeForLifecycleStage(stage) value with Source ModeSourceLifecycle when
// no explicit row exists or the stored incident row's expiry has passed
// on s.clock (Art.7.3 -- never a sleep-based check). Resolve never
// mutates the stored row: an expired incident is a virtual computed
// result, not a write.
func (s *SchedulerModeStore) Resolve(ctx context.Context, projectID string, stage string) (ModeState, error) {
	if projectID == "" {
		return ModeState{}, cascade.New(cascade.KindInvalidInput, "economics: Resolve requires a non-empty project_id")
	}
	row, ok, err := s.selectModeRow(ctx, projectID)
	if err != nil {
		return ModeState{}, err
	}
	if ok {
		expired := row.mode == string(ModeIncident) && row.expiresAt.Valid &&
			!s.clock.Now().Before(time.Unix(row.expiresAt.Int64, 0))
		if !expired {
			state := ModeState{ProjectID: projectID, Mode: Mode(row.mode), Source: ModeSourceExplicit, SetAt: time.Unix(row.setAt, 0)}
			if row.expiresAt.Valid {
				state.ExpiresAt = time.Unix(row.expiresAt.Int64, 0)
			}
			return state, nil
		}
	}
	m, err := ModeForLifecycleStage(stage)
	if err != nil {
		return ModeState{}, err
	}
	return ModeState{ProjectID: projectID, Mode: m, Source: ModeSourceLifecycle}, nil
}

// Clear removes projectID's explicit row entirely, resetting the
// R-21.118 renewal counters to zero (there is no row left to carry a
// non-zero value) and reverting Resolve to its lifecycle-derived default.
func (s *SchedulerModeStore) Clear(ctx context.Context, projectID string) error {
	if projectID == "" {
		return cascade.New(cascade.KindInvalidInput, "economics: Clear requires a non-empty project_id")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+tableSchedulerMode+` WHERE project_id = ?`, projectID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: delete scheduler_mode")
	}
	return nil
}
