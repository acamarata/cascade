package repo

// Purpose: the repo domain's persisted CRUD surface. R-21.22 keeps the
//   R-14.5/R-16.51 domain list CLOSED (contract/tree contradiction, full
//   quote in this ticket's journal): this ticket's own text directs
//   "register the repo domain owner in the storage domain-ownership
//   registry", but internal/storage/domains.go's eleven-domain
//   enumeration is closed by ruling and R-21.22 resolves exactly this
//   situation by adding new tables to an EXISTING domain rather than a
//   new DomainID. Inventory therefore persists as one additional
//   table in the `context` domain (context_repo_inventory), the same
//   domain internal/context/scope already owns context_repository/
//   context_repo_path in, with a foreign key onto context_repository.id
//   -- consistent with R-16.3's own statement that repository identity
//   lives in the context domain. internal/storage/domains.go's change
//   (files_scope) is therefore a documentation-only edit crediting this
//   package as an additional owner of the context domain's tables, never
//   a new DomainID constant.
// Inputs: an open *sql.DB already migrated via ApplyInventorySchema
//   (which must run after internal/context/scope's ApplyScopeSchema, so
//   the foreign key's target table exists).
// Outputs: typed A-T7 errors for malformed input or storage failure;
//   ErrNotFound-shaped (ok=false, nil error) for a missing row, matching
//   internal/jobs's GetJob convention.
// Constraints: Inventory is stored as one JSON blob per repository
//   (languages/layout/CI/harness/membership) rather than one table per
//   nested slice -- the alternative (a table per Languages/Membership
//   entry) is straightforward with the same DSL but was out of reach
//   within this ticket's fixed files_scope + 300-line-per-file cap
//   alongside the six detector families; store_test.go's round-trip test
//   proves the simplification loses no information. A future ticket that
//   needs to query inventory by individual language (e.g. "every repo
//   with rust") is the natural point to normalize this into per-language
//   rows.
// SPORT: repo/inventory-store/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableInventory = "context_repo_inventory"

// repoInventorySchemaVersion is this package's MigrationSet target
// version -- the next unused slot in cascade.db's single global
// schema_version sequence (see internal/context/scope/schema.go's doc
// comment for why the sequence is global). Claimed slots as of this
// ticket: bootstrap=1, context/scope=2, retrieval/lifecycle=3,
// providers/registry=4, jobs=5, providers/usage=6. This package claims 7.
const repoInventorySchemaVersion = 7

// SchemaVersion is repoInventorySchemaVersion exported for a future
// composition root's reader-ceiling max(), matching every sibling
// domain-schema package's own exported-for-the-same-reason pattern.
const SchemaVersion = repoInventorySchemaVersion

// MigrationSet is the one-table schema this package adds to the context
// domain.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        repoInventorySchemaVersion,
		MinimumReaderVersion: repoInventorySchemaVersion,
		Steps:                []migrate.MigrationStep{repoInventoryTableStep()},
	}
}

func repoInventoryTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_repo_inventory: one row per repository's deterministic inventory snapshot",
		Table: &migrate.TableDef{
			Name: tableInventory,
			Columns: []migrate.ColumnDef{
				{Name: "repository_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "inventory_json", Type: migrate.TypeText, NotNull: true},
				{Name: "scanned_at", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "repository_id", RefTable: "context_repository", RefColumn: "id"},
			},
		},
	}
}

// ApplyInventorySchema idempotently applies MigrationSet against db.
// Must run after internal/context/scope's ApplyScopeSchema in the same
// db (the foreign key target must already exist).
func ApplyInventorySchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyInventorySchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyInventorySchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, MigrationSet())
}

// Store is the repo domain's persisted CRUD surface.
type Store struct{ db *sql.DB }

// NewStore wraps db. db must already carry MigrationSet's table
// (ApplyInventorySchema) and internal/context/scope's tables.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Upsert inserts or fully replaces one Inventory row, keyed by
// inv.Repository.ID.
func (s *Store) Upsert(ctx context.Context, inv Inventory) error {
	if inv.Repository.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: inventory repository id is required")
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "repo: encode inventory")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableInventory+` (repository_id, inventory_json, scanned_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(repository_id) DO UPDATE SET
		   inventory_json=excluded.inventory_json, scanned_at=excluded.scanned_at`,
		inv.Repository.ID, string(data), inv.ScannedAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repo: upsert inventory")
	}
	return nil
}

// Get reads one Inventory by repository id. ok=false with a nil error
// means "no such row", never a cascade.Error.
func (s *Store) Get(ctx context.Context, repositoryID string) (Inventory, bool, error) {
	if repositoryID == "" {
		return Inventory{}, false, cascade.New(cascade.KindInvalidInput, "repo: Get requires a non-empty repository id")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT inventory_json FROM `+tableInventory+` WHERE repository_id = ?`, repositoryID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return Inventory{}, false, nil
		}
		return Inventory{}, false, cascade.Wrap(cascade.KindUnavailable, err, "repo: get inventory")
	}
	var inv Inventory
	if err := json.Unmarshal([]byte(raw), &inv); err != nil {
		return Inventory{}, false, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode stored inventory")
	}
	return inv, true, nil
}

// List returns every stored Inventory, ordered by repository_id for
// determinism.
func (s *Store) List(ctx context.Context) ([]Inventory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT inventory_json FROM `+tableInventory+` ORDER BY repository_id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: list inventory")
	}
	defer func() { _ = rows.Close() }()

	var out []Inventory
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: scan inventory row")
		}
		var inv Inventory
		if err := json.Unmarshal([]byte(raw), &inv); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode stored inventory")
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: iterate inventory rows")
	}
	return out, nil
}
