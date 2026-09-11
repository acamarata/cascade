package repo

// Purpose: persisted CRUD for InferredFact records -- Append (a new
//   proposal or a new version), Get (by id+version or latest), List
//   (filtered by repository/subject/state), UpdateState (accept/reject/
//   supersede transitions). Follows store.go/graph_store.go's
//   established shape: one JSON blob per row, its own migrate.
//   MigrationSet under a distinct SetID.
// Inputs: an open *sql.DB migrated via ApplyLedgerSchema.
// Outputs: typed A-T7 errors for malformed input or storage failure;
//   ok=false (nil error) for a missing row.
// Constraints: a distinct SetID ("repo-ledger") from store.go's "repo"
//   and graph_store.go's "repo-graph", for the same reason graph_store.go
//   documents (R-16.77; no tree precedent for extending one package's
//   existing SetID to a new SchemaVersion from a later ticket).
// SPORT: repo/inferred-fact-ledger/ADD (P1-E33-W7-S67-T2).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableLedger = "context_repo_fact_ledger"

const ledgerSchemaVersion = 1

// LedgerMigrationSet is the one-table schema this file adds.
func LedgerMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "repo-ledger",
		SchemaVersion: ledgerSchemaVersion,
		ReaderCeiling: ledgerSchemaVersion,
		Steps:         []migrate.MigrationStep{ledgerTableStep()},
	}
}

func ledgerTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_repo_fact_ledger: one row per InferredFact version",
		Table: &migrate.TableDef{
			Name: tableLedger,
			Columns: []migrate.ColumnDef{
				{Name: "row_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "fact_id", Type: migrate.TypeText, NotNull: true},
				{Name: "repository_id", Type: migrate.TypeText, NotNull: true},
				{Name: "version", Type: migrate.TypeInteger, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
				{Name: "fact_json", Type: migrate.TypeText, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "repository_id", RefTable: "context_repository", RefColumn: "id"},
			},
		},
	}
}

// ApplyLedgerSchema idempotently applies LedgerMigrationSet against db.
func ApplyLedgerSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyLedgerSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyLedgerSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, LedgerMigrationSet())
}

// LedgerStore is the repo domain's persisted InferredFact CRUD surface.
type LedgerStore struct{ db *sql.DB }

// NewLedgerStore wraps db. db must already carry LedgerMigrationSet's
// table (ApplyLedgerSchema).
func NewLedgerStore(db *sql.DB) *LedgerStore { return &LedgerStore{db: db} }

// rowID is the unique per-version storage key: fact id + version never
// collide across two different facts, so simple concatenation is a safe
// primary key without a separate surrogate counter.
func rowID(factID string, version int) string {
	return factID + "@" + strconv.Itoa(version)
}

// Append inserts a new InferredFact row (a new proposal or a new
// version). Fails on a duplicate (fact id, version) pair -- Append never
// overwrites an existing version; supersede/rollback are explicit
// UpdateState transitions, not silent replacement.
func (s *LedgerStore) Append(ctx context.Context, f InferredFact) error {
	if err := f.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "repo: encode inferred fact")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableLedger+` (row_id, fact_id, repository_id, version, state, fact_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rowID(f.ID, f.Version), f.ID, f.RepositoryID, f.Version, string(f.State), string(data))
	if err != nil {
		return cascade.Wrap(cascade.KindConflict, err, "repo: append inferred fact")
	}
	return nil
}

// Get reads one InferredFact by id and version. ok=false with a nil
// error means "no such row".
func (s *LedgerStore) Get(ctx context.Context, factID string, version int) (InferredFact, bool, error) {
	if factID == "" {
		return InferredFact{}, false, cascade.New(cascade.KindInvalidInput, "repo: Get requires a non-empty fact id")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT fact_json FROM `+tableLedger+` WHERE row_id = ?`, rowID(factID, version))
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return InferredFact{}, false, nil
		}
		return InferredFact{}, false, cascade.Wrap(cascade.KindUnavailable, err, "repo: get inferred fact")
	}
	var f InferredFact
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return InferredFact{}, false, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode stored inferred fact")
	}
	return f, true, nil
}

// LedgerFilter narrows List to one repository and, optionally, one
// subject or state. A zero-value field means "no filter on that axis".
type LedgerFilter struct {
	RepositoryID string
	Subject      FactSubject
	State        FactState
}

// List returns every stored InferredFact matching filter, ordered by
// fact id then version for determinism.
func (s *LedgerStore) List(ctx context.Context, filter LedgerFilter) ([]InferredFact, error) {
	if filter.RepositoryID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: List requires a non-empty repository id")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT fact_json FROM `+tableLedger+` WHERE repository_id = ? ORDER BY fact_id, version`,
		filter.RepositoryID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: list inferred facts")
	}
	defer func() { _ = rows.Close() }()

	var out []InferredFact
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: scan inferred fact row")
		}
		var f InferredFact
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode stored inferred fact")
		}
		if filter.Subject != "" && f.Subject != filter.Subject {
			continue
		}
		if filter.State != "" && f.State != filter.State {
			continue
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: iterate inferred fact rows")
	}
	return out, nil
}

// UpdateState rewrites one row's state (and, for a rejection, its
// RejectReason / for an acceptance, AcceptedBy+Rollback), by loading,
// mutating, and re-storing the full record -- never a partial column
// UPDATE that could drift from the JSON blob's own copy of State.
func (s *LedgerStore) UpdateState(ctx context.Context, f InferredFact) error {
	if err := f.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "repo: encode inferred fact")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+tableLedger+` SET state = ?, fact_json = ? WHERE row_id = ?`,
		string(f.State), string(data), rowID(f.ID, f.Version))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repo: update inferred fact state")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repo: read update result")
	}
	if n == 0 {
		return cascade.Newf(cascade.KindNotFound, "repo: no inferred fact %s@%d to update", f.ID, f.Version)
	}
	return nil
}
