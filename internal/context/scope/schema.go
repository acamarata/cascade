package scope

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: MigrationSet defines exactly the four `context` domain
//   tables R-16.3 ratifies -- scope, scope_edge, repository, repo_path --
//   through the B/S-02.T2 typed migration DSL, portable to both the
//   SQLite (local) and Postgres (server) dialects; ApplyScopeSchema runs
//   it idempotently against an already-bootstrapped context domain.
// Inputs: an open *sql.DB whose `context` domain anchor table already
//   exists (internal/storage.Bootstrap), a migrate.Dialect, an injected
//   migrate.Clock, and the DB's on-disk path (for the SQLite snapshot;
//   empty disables it).
// Outputs: nil on success (idempotent re-apply is a no-op); a
//   *cascade.Error or a migrate typed error otherwise.
// Constraints: no fifth table, no parallel sessions domain -- this ticket
//   adds exactly these four tables and nothing else.
// SPORT: context/scope-graph/ADD.

// Table names, prefixed per storage/domains.go's TablePrefix convention
// for the `context` domain ("context_" + <name>) so a `context.<x>` and a
// future unrelated domain's same-named table never collide in
// sqlite_master.
const (
	tableScope      = "context_scope"
	tableScopeEdge  = "context_scope_edge"
	tableRepository = "context_repository"
	tableRepoPath   = "context_repo_path"
)

// scopeSchemaVersion is this package's MigrationSet target version.
//
// Contract/tree correction (see this ticket's journal for the full quoted
// contradiction): this constant WAS 1, on the assumption that
// migrate.Apply's ledger keys schema_version per calling MigrationSet.
// It does not -- ledger.go's verifyAppliedPrefix/ledgerRowsForVersion
// read the SAME applied_migrations table by schema_version alone, with
// no MigrationSet-identity column, so every MigrationSet ever applied to
// one db file shares ONE global version sequence. cascade.db's
// bootstrapSchemaVersion (internal/storage/domains.go) already claims
// version 1 -- storage.Bootstrap runs before any domain-specific
// ApplyScopeSchema call on the daemon's real db (openRuntimeStore, then
// buildRPCServer), so a scope schema also targeting version 1 hit a real
// *MigrationConflictError the first time this package's ApplyScopeSchema
// ran against that already-bootstrapped file: "checksum conflict at
// schema_version 1 step 0: ledger has bootstrap-storage..., current
// definition hashes to <scope step 0's hash>". Version 2 is the first
// version number no other domain has claimed yet (grep across internal/
// for `SchemaVersion:` finds only bootstrapSchemaVersion=1 and this
// package, before this fix, at the same value).
const scopeSchemaVersion = 2

// SchemaVersion is scopeSchemaVersion exported for the composition root.
// Because the applied_migrations ledger has no MigrationSet-identity
// column, every domain schema in one db file shares ONE global version
// sequence (see scopeSchemaVersion's comment). The composition root must
// therefore declare a reader ceiling covering the HIGHEST version any
// bundled set targets, and it reads that value from here rather than
// duplicating the literal, so the two can never drift (R-14.198).
const SchemaVersion = scopeSchemaVersion

// MigrationSet is the four-table `context` domain schema R-16.3
// ratifies. kind/from_ref_kind/etc. are stored as TEXT (the DSL has no
// enum column type); model.go's EdgeKind/Kind Valid() gates are the
// application-level closed-vocabulary enforcement store.go applies before
// any row reaches this schema, matching the fail-closed pattern used
// throughout this package.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        scopeSchemaVersion,
		MinimumReaderVersion: scopeSchemaVersion,
		Steps: []migrate.MigrationStep{
			repositoryTableStep(),
			repoPathTableStep(),
			scopeTableStep(),
			scopeEdgeTableStep(),
		},
	}
}

// repositoryTableStep is the context_repository create-table step: one row
// per resolved git repository (remote + path hash).
func repositoryTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_repository: one row per resolved git repository (remote + path hash)",
		Table: &migrate.TableDef{
			Name: tableRepository,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "remote", Type: migrate.TypeText, NotNull: true},
				{Name: "path_hash", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

// repoPathTableStep is the context_repo_path create-table step: one local
// filesystem anchor bound to a repository.
func repoPathTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_repo_path: local filesystem anchor -> repository binding",
		Table: &migrate.TableDef{
			Name: tableRepoPath,
			Columns: []migrate.ColumnDef{
				{Name: "root_path", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "repository_id", Type: migrate.TypeText, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "repository_id", RefTable: tableRepository, RefColumn: "id"},
			},
		},
	}
}

// scopeTableStep is the context_scope create-table step: one row per scope
// graph node, addressed by (kind, id).
func scopeTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_scope: one row per scope graph node, addressed by (kind, id)",
		Table: &migrate.TableDef{
			Name: tableScope,
			Columns: []migrate.ColumnDef{
				{Name: "kind", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "display_name", Type: migrate.TypeText},
			},
		},
	}
}

// scopeEdgeTableStep is the context_scope_edge create-table step: explicit
// depends_on/member_of/shares_context_with relationships.
func scopeEdgeTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "context_scope_edge: explicit depends_on/member_of/shares_context_with relationships",
		Table: &migrate.TableDef{
			Name: tableScopeEdge,
			Columns: []migrate.ColumnDef{
				{Name: "from_kind", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "from_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "to_kind", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "to_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "edge_kind", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

// ApplyScopeSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty; either
// may be left empty to disable it (an in-memory test database has nothing
// to snapshot).
func ApplyScopeSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "context/scope: ApplyScopeSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "context/scope: ApplyScopeSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        db,
		Dialect:   dialect,
		Clock:     clock,
		DBPath:    dbPath,
		BackupDir: backupDir,
	}, MigrationSet())
}
