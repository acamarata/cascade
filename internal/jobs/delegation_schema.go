package jobs

// Purpose: the jobs_delegation table's schema (contract:delegation-record):
//
//	one insert-only row per delegation holding its controller-signed scope
//	envelope and its parent's, with set-once marker and spawn bindings and
//	a live -> terminal state. Authored through the migration builder under
//	its own SetID, following migration_outbox.go.
//
// SetID: "jobs", "jobs-evidence" and "jobs-outbox" are claimed; this file
// claims the distinct "jobs-delegation" SetID at schema_version 1. The
// migration is additive (one new table and its indexes); a binary that
// predates it never reads the table.
//
// TRIGGERS: the migration DSL has no CHECK or trigger support, so
// ApplyDelegationSchema adds the triggers after the set applies, with
// CREATE TRIGGER IF NOT EXISTS so a re-apply is a no-op. Whatever path
// issues the statement, they refuse at the database: any UPDATE that
// changes a record field, a second marker or spawn binding, a spawn
// binding before the marker (M-4), any change to a terminal row, an
// unknown state, an INSERT that is not a fresh live row, and every
// DELETE. The triggers are SQLite DDL; another dialect is refused
// (KindUnsupported) rather than left without them.
//
// SPORT: jobs/delegation-record/ADD (P1-AGT-11).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

const tableDelegation = "jobs_delegation"

const delegationSchemaVersion = 1

// DelegationMigrationSet is the jobs_delegation table's own MigrationSet,
// distinct from the other jobs sets per R-16.77.
func DelegationMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-delegation",
		SchemaVersion: delegationSchemaVersion,
		ReaderCeiling: delegationSchemaVersion,
		Steps: []migrate.MigrationStep{
			delegationTableStep(),
			delegationIndex("jobs_delegation_digest_idx", true, "envelope_digest"),
			delegationIndex("jobs_delegation_agent_idx", true, "agent_principal"),
			delegationIndex("jobs_delegation_parent_state_idx", false, "parent_digest", "state"),
		},
	}
}

// delegationIndex is one CREATE INDEX step on the delegation table. The
// unique digest index refuses a replayed Signed under a new ID; the unique
// agent index makes DelegationByAgentPrincipal answer at most one record.
func delegationIndex(name string, unique bool, cols ...string) migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateIndex,
		Description: "jobs_delegation: " + name,
		Index:       &migrate.IndexDef{Name: name, Table: tableDelegation, Columns: cols, Unique: unique},
	}
}

func delegationTableStep() migrate.MigrationStep {
	text := func(n string, notNull bool) migrate.ColumnDef {
		return migrate.ColumnDef{Name: n, Type: migrate.TypeText, NotNull: notNull}
	}
	integer := func(n string) migrate.ColumnDef {
		return migrate.ColumnDef{Name: n, Type: migrate.TypeInteger, NotNull: true}
	}
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_delegation: the signed, insert-only delegation record",
		Table: &migrate.TableDef{
			Name: tableDelegation,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("envelope_digest", true),
				{Name: "signed", Type: migrate.TypeBlob, NotNull: true},
				text("parent_digest", true),
				{Name: "parent_signed", Type: migrate.TypeBlob},
				text("origin_principal", true), text("origin_session", true),
				text("agent_principal", true), integer("depth"),
				text("job_id", true), integer("mutation"), text("lane", true),
				text("lease_id", false), text("agent_job_id", false), text("lease_marker", false),
				text("state", true), text("reason", true),
				integer("created_at"), integer("updated_at"),
			},
		},
	}
}

// Trigger messages. Each names the rule the database refused.
const (
	msgDelegationImmutable = "jobs_delegation: record fields are immutable"
	msgDelegationSpawnOnce = "jobs_delegation: spawn binding is set once"
	msgDelegationMarkOnce  = "jobs_delegation: lease marker is set once"
	msgDelegationMarkFirst = "jobs_delegation: spawn binding requires the lease marker first"
	msgDelegationTerminal  = "jobs_delegation: a terminal record is final"
	msgDelegationState     = "jobs_delegation: refused state or binding"
	msgDelegationNoDelete  = "jobs_delegation: records are insert-only"
)

// delegationTriggers make the record insert-only at the database.
var delegationTriggers = []string{
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_immutable BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN NEW.id IS NOT OLD.id OR NEW.envelope_digest IS NOT OLD.envelope_digest OR NEW.signed IS NOT OLD.signed
	   OR NEW.parent_digest IS NOT OLD.parent_digest OR NEW.parent_signed IS NOT OLD.parent_signed
	   OR NEW.origin_principal IS NOT OLD.origin_principal OR NEW.origin_session IS NOT OLD.origin_session
	   OR NEW.agent_principal IS NOT OLD.agent_principal OR NEW.depth IS NOT OLD.depth
	   OR NEW.job_id IS NOT OLD.job_id OR NEW.mutation IS NOT OLD.mutation OR NEW.lane IS NOT OLD.lane
	   OR NEW.created_at IS NOT OLD.created_at
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationImmutable + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_spawn_once BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN OLD.lease_id IS NOT NULL AND (NEW.lease_id IS NOT OLD.lease_id OR NEW.agent_job_id IS NOT OLD.agent_job_id)
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationSpawnOnce + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_marker_once BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN OLD.lease_marker IS NOT NULL AND NEW.lease_marker IS NOT OLD.lease_marker
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationMarkOnce + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_marker_first BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN NEW.lease_id IS NOT NULL AND NEW.lease_marker IS NULL
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationMarkFirst + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_terminal BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN OLD.state <> 'live' AND (NEW.state IS NOT OLD.state OR NEW.reason IS NOT OLD.reason
	   OR NEW.lease_id IS NOT OLD.lease_id OR NEW.agent_job_id IS NOT OLD.agent_job_id
	   OR NEW.lease_marker IS NOT OLD.lease_marker)
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationTerminal + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_state_update BEFORE UPDATE ON ` + tableDelegation + `
	 WHEN NEW.state NOT IN ('live', 'succeeded', 'failed', 'cancelled', 'refused')
	   OR (NEW.state = 'live' AND NEW.reason IS NOT OLD.reason)
	   OR ((NEW.lease_id IS NULL) <> (NEW.agent_job_id IS NULL))
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationState + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_state_insert BEFORE INSERT ON ` + tableDelegation + `
	 WHEN NEW.state <> 'live' OR NEW.lease_id IS NOT NULL OR NEW.agent_job_id IS NOT NULL
	   OR NEW.lease_marker IS NOT NULL OR NEW.reason <> ''
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationState + `'); END`,
	`CREATE TRIGGER IF NOT EXISTS jobs_delegation_no_delete BEFORE DELETE ON ` + tableDelegation + `
	 BEGIN SELECT RAISE(ABORT, '` + msgDelegationNoDelete + `'); END`,
}

// ApplyDelegationSchema applies DelegationMigrationSet against db and then
// the record's triggers, following ApplyOutboxSchema's parameter shape and
// nil-guards.
func ApplyDelegationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyDelegationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyDelegationSchema requires a non-nil Clock")
	}
	if dialect == nil || dialect.Name() != "sqlite" {
		return cascade.New(cascade.KindUnsupported, "jobs: the delegation record's triggers are SQLite only")
	}
	err := migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, DelegationMigrationSet())
	if err != nil {
		return err
	}
	for _, stmt := range delegationTriggers {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: create delegation trigger")
		}
	}
	return nil
}

// delegationCols is the column list every read selects, in scan order.
const delegationCols = `id, envelope_digest, signed, parent_digest, parent_signed, origin_principal,
	origin_session, agent_principal, depth, job_id, mutation, lane, lease_id, agent_job_id,
	lease_marker, state, reason, created_at, updated_at`

// rowScanner is the Scan method *sql.Row and *sql.Rows share.
type rowScanner interface{ Scan(dest ...any) error }

// scanDelegation reads one record; ok is false when there is no row.
func scanDelegation(r rowScanner) (rec DelegationRecord, ok bool, err error) {
	var lease, agentJob, marker sql.NullString
	var mutation int
	var state string
	var created, updated int64
	err = r.Scan(&rec.ID, &rec.EnvelopeDigest, &rec.Signed, &rec.ParentDigest, &rec.ParentSigned,
		&rec.OriginPrincipal, &rec.OriginSession, &rec.AgentPrincipal, &rec.Depth, &rec.JobID,
		&mutation, &rec.Lane, &lease, &agentJob, &marker, &state, &rec.Reason, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return DelegationRecord{}, false, nil
	}
	if err != nil {
		return DelegationRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read delegation")
	}
	rec.Mutation, rec.State = mutation != 0, DelegationState(state)
	rec.LeaseID, rec.AgentJobID, rec.LeaseMarker = lease.String, agentJob.String, marker.String
	rec.CreatedAt, rec.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return rec, true, nil
}

// firstHopRoot accepts an unstored parent only when it is a root envelope
// whose digest is the child's ParentDigest; it returns the root principal.
func firstHopRoot(env envelope.Envelope, parentSigned []byte) (string, error) {
	if len(parentSigned) == 0 {
		return "", cascade.Newf(cascade.KindNotFound, "jobs: delegation %q names an unknown parent envelope", env.ID)
	}
	root, err := envelope.Decode(parentSigned)
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "jobs: ParentSigned does not decode")
	}
	if d, err := envelope.Digest(root.Env); err != nil || d != env.ParentDigest {
		return "", cascade.New(cascade.KindIntegrity, "jobs: ParentSigned does not hash to ParentDigest")
	}
	if root.Env.ParentDigest != "" {
		return "", cascade.Newf(cascade.KindNotFound, "jobs: parent of %q is neither a stored record nor a root envelope", env.ID)
	}
	if root.Env.ID == env.ID {
		return "", cascade.Newf(cascade.KindIntegrity, "jobs: cycle: delegation %q reuses its parent's ID", env.ID)
	}
	return root.Env.Principal, nil
}

// insertDelegationTx inserts row. For a child, the INSERT carries the
// live-children count as its own condition, so the check and the write are
// one statement under the database's single write lock.
func insertDelegationTx(ctx context.Context, tx *sql.Tx, row DelegationRecord) error {
	var parentSigned any
	if len(row.ParentSigned) != 0 {
		parentSigned = row.ParentSigned
	}
	mutation := 0
	if row.Mutation {
		mutation = 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO `+tableDelegation+` (`+delegationCols+`)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, 'live', '', ?, ?
		 WHERE ? = '' OR (SELECT COUNT(*) FROM `+tableDelegation+` WHERE parent_digest = ? AND state = 'live') < ?`,
		row.ID, row.EnvelopeDigest, row.Signed, row.ParentDigest, parentSigned, row.OriginPrincipal,
		row.OriginSession, row.AgentPrincipal, row.Depth, row.JobID, mutation, row.Lane,
		row.CreatedAt.UnixNano(), row.UpdatedAt.UnixNano(),
		row.ParentDigest, row.ParentDigest, DelegationMaxLiveChildren)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return cascade.Newf(cascade.KindConflict, "jobs: delegation %q, its envelope or its agent principal is already recorded", row.ID)
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert delegation")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cascade.Newf(cascade.KindPermissionDenied,
			"jobs: fan-out: parent %s already holds MaxLiveChildren (%d) live children", row.ParentDigest, DelegationMaxLiveChildren)
	}
	return nil
}
