package repo

// Purpose: persisted CRUD for the extracted SymbolGraph, keyed by
//   repository + scan generation so a rescan replaces the prior graph
//   atomically. Follows store.go's established shape: one JSON blob per
//   row, its own migrate.MigrationSet.
// Inputs: an open *sql.DB migrated via ApplyGraphSchema.
// Outputs: typed A-T7 errors for malformed input or storage failure;
//   ok=false (nil error) for a missing row, matching store.go's Get
//   convention.
// Constraints: a distinct SetID ("repo-graph") from store.go's "repo" --
//   R-16.77 lets two MigrationSets share a SchemaVersion namespace only
//   when their SetIDs differ, and no precedent in this tree extends an
//   existing package's SetID to a second SchemaVersion from a second
//   ticket; a fresh SetID avoids that untested assumption while still
//   living in the same package and the same "context" storage domain
//   store.go's own comment already established (R-21.22).
// SPORT: repo/symbol-dependency-graph/ADD (P1-E33-W7-S67-T3).

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableGraph = "context_repo_graph"

// graphSchemaVersion is this package's MigrationSet target version within
// its own SetID ("repo-graph"), independent of store.go's "repo" SetID
// sequence (R-16.77). Kept unexported, unlike store.go's exported
// SchemaVersion alias: that alias's bare identifier happens to collide
// textually with the ubiquitous MigrationSet.SchemaVersion struct field
// name across the tree, which is what keeps internal/build's dead-code
// scan (a bare-identifier text match, not a type-aware one) from flagging
// it; GraphSchemaVersion has no such coincidental match and would be a
// real dead export with no caller.
const graphSchemaVersion = 1

// GraphMigrationSet is the one-table schema this file adds.
func GraphMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "repo-graph",
		SchemaVersion: graphSchemaVersion,
		ReaderCeiling: graphSchemaVersion,
		Steps:         []migrate.MigrationStep{graphTableStep()},
	}
}

func graphTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_repo_graph: one row per repository's latest symbol/dependency graph",
		Table: &migrate.TableDef{
			Name: tableGraph,
			Columns: []migrate.ColumnDef{
				{Name: "repository_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "generation", Type: migrate.TypeInteger, NotNull: true},
				{Name: "graph_json", Type: migrate.TypeText, NotNull: true},
				{Name: "scanned_at", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "repository_id", RefTable: "context_repository", RefColumn: "id"},
			},
		},
	}
}

// ApplyGraphSchema idempotently applies GraphMigrationSet against db.
// Must run after internal/context/scope's ApplyScopeSchema in the same
// db (the foreign key target must already exist).
func ApplyGraphSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyGraphSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyGraphSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, GraphMigrationSet())
}

// GraphStore is the repo domain's persisted SymbolGraph CRUD surface.
type GraphStore struct{ db *sql.DB }

// NewGraphStore wraps db. db must already carry GraphMigrationSet's table
// (ApplyGraphSchema).
func NewGraphStore(db *sql.DB) *GraphStore { return &GraphStore{db: db} }

// graphRow is the on-disk row shape, wrapping SymbolGraph with the
// generation counter a rescan bumps.
type graphRow struct {
	Generation int         `json:"generation"`
	Graph      SymbolGraph `json:"graph"`
}

// Upsert atomically replaces repositoryID's stored graph with g at
// generation, stamped with scannedAt (the caller's own injected Clock
// reading -- A-T4: this file never calls time.Now itself). A rescan
// supplies a strictly greater generation than the prior stored row;
// Upsert does not itself enforce monotonicity (the caller -- the rescan
// driver -- owns generation assignment), but the single-row REPLACE means
// no partial-graph read is ever possible: a reader sees either the
// complete prior graph or the complete new one, never a mix.
func (s *GraphStore) Upsert(ctx context.Context, repositoryID string, generation int, g SymbolGraph, scannedAt int64) error {
	if repositoryID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: graph upsert requires a non-empty repository id")
	}
	if err := g.Validate(); err != nil {
		return err
	}
	row := graphRow{Generation: generation, Graph: g}
	data, err := json.Marshal(row)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "repo: encode graph")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableGraph+` (repository_id, generation, graph_json, scanned_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(repository_id) DO UPDATE SET
		   generation=excluded.generation, graph_json=excluded.graph_json, scanned_at=excluded.scanned_at`,
		repositoryID, generation, string(data), scannedAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repo: upsert graph")
	}
	return nil
}

// Get reads the current SymbolGraph and its generation for repositoryID.
// ok=false with a nil error means "no such row".
func (s *GraphStore) Get(ctx context.Context, repositoryID string) (SymbolGraph, int, bool, error) {
	if repositoryID == "" {
		return SymbolGraph{}, 0, false, cascade.New(cascade.KindInvalidInput, "repo: Get requires a non-empty repository id")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT graph_json FROM `+tableGraph+` WHERE repository_id = ?`, repositoryID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return SymbolGraph{}, 0, false, nil
		}
		return SymbolGraph{}, 0, false, cascade.Wrap(cascade.KindUnavailable, err, "repo: get graph")
	}
	var gr graphRow
	if err := json.Unmarshal([]byte(raw), &gr); err != nil {
		return SymbolGraph{}, 0, false, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode stored graph")
	}
	return gr.Graph, gr.Generation, true, nil
}

// List returns every stored repository id with a graph, ordered for
// determinism.
func (s *GraphStore) List(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT repository_id FROM `+tableGraph+` ORDER BY repository_id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: list graph repositories")
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: scan graph repository row")
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: iterate graph repository rows")
	}
	return out, nil
}
