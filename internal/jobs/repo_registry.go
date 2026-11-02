package jobs

// Purpose: repository identity lookup and append-only CI gate bindings.
// Inputs: scope identity graph, injected git common-dir probe and clock.
// Outputs: shared repository ids and signed operator registrations.
// Constraints: scope owns identity; CI never derives an identity from a path.
// SPORT: jobs/repository-registry/ADD.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// ErrRepoNotRegistered reports a missing repository registration or gate.
var ErrRepoNotRegistered = cascade.New(cascade.KindNotFound, "repository not registered")

// BranchTip records a full local branch ref and its full object id.
type BranchTip struct{ Ref, Commit string }

// BranchTipsFunc reads the branch tips the operator intends to attest.
type BranchTipsFunc func(context.Context, string) ([]BranchTip, error)

// RepoRegistration distinguishes indexed identity from operator trust.
type RepoRegistration struct {
	RepoID             string
	OperatorRegistered bool
	RegisteredAt       time.Time
	Baseline           []BranchTip
}

// RepoRegistry binds CI gates and signed trust records to scope identities.
type RepoRegistry struct {
	db     *sql.DB
	graph  *scope.GraphStore
	git    scope.GitCommonDirFunc
	clock  runtime.Clock
	signer envelope.Signer
}

// NewRepoRegistry wraps an already migrated database and identity graph.
func NewRepoRegistry(db *sql.DB, graph *scope.GraphStore, git scope.GitCommonDirFunc, clock runtime.Clock) (*RepoRegistry, error) {
	if db == nil || graph == nil || git == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repository registry needs db, graph, git and clock")
	}
	return &RepoRegistry{db: db, graph: graph, git: git, clock: clock}, nil
}

// ApplyRepoRegistrySchema installs scope before the CI registry migration.
func ApplyRepoRegistrySchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if err := scope.ApplyScopeSchema(ctx, db, dialect, clock, dbPath, backupDir); err != nil {
		return err
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: repoRegistryDialect{dialect}, Clock: clock, DBPath: dbPath, BackupDir: backupDir}, repoRegistryMigrationSet())
}

// repoRegistryDialect adds the trust tip's portable object-id constraint,
// which the shared CREATE-only DSL does not otherwise express.
type repoRegistryDialect struct{ migrate.Dialect }

func (d repoRegistryDialect) Emit(set migrate.MigrationSet) ([]string, error) {
	statements, err := d.Dialect.Emit(set)
	for i, stmt := range statements {
		if strings.HasPrefix(stmt, `CREATE TABLE IF NOT EXISTS "jobs_repo_trust_fetch_tip"`) {
			end := strings.LastIndex(stmt, ")")
			statements[i] = stmt[:end] + `, CHECK (length(commit_id) IN (40, 64))` + stmt[end:]
		}
	}
	return statements, err
}

func repoRegistryMigrationSet() migrate.MigrationSet {
	tables := []struct {
		name             string
		keys, text, blob []string
		fk               []migrate.ForeignKeyDef
	}{
		{"jobs_repo_gate", []string{"gate_path"}, []string{"repo_id", "installed_at"}, nil, []migrate.ForeignKeyDef{{Column: "repo_id", RefTable: "context_repository", RefColumn: "id"}}},
		{"jobs_repo_registration", []string{"repo_id"}, []string{"root", "registered_at"}, []string{"sig"}, []migrate.ForeignKeyDef{{Column: "repo_id", RefTable: "context_repository", RefColumn: "id"}}},
		{"jobs_repo_baseline", []string{"repo_id", "ref"}, []string{"commit_id"}, []string{"sig"}, []migrate.ForeignKeyDef{{Column: "repo_id", RefTable: "jobs_repo_registration", RefColumn: "repo_id"}}},
		{"jobs_repo_trust_fetch", []string{"repo_id", "remote_id"}, []string{"url", "fetched_at"}, nil, []migrate.ForeignKeyDef{{Column: "repo_id", RefTable: "jobs_repo_registration", RefColumn: "repo_id"}}},
		{"jobs_repo_trust_fetch_tip", []string{"repo_id", "remote_id", "ref"}, []string{"commit_id"}, []string{"sig"}, nil},
	}
	set := migrate.MigrationSet{SetID: "jobs-repo-registry", SchemaVersion: 1, ReaderCeiling: 1}
	for _, t := range tables {
		def := &migrate.TableDef{Name: t.name, ForeignKeys: t.fk}
		for _, n := range t.keys {
			def.Columns = append(def.Columns, migrate.ColumnDef{Name: n, Type: migrate.TypeText, PrimaryKey: true, NotNull: true})
		}
		for _, n := range t.text {
			def.Columns = append(def.Columns, migrate.ColumnDef{Name: n, Type: migrate.TypeText, NotNull: true})
		}
		for _, n := range t.blob {
			def.Columns = append(def.Columns, migrate.ColumnDef{Name: n, Type: migrate.TypeBlob})
		}
		set.Steps = append(set.Steps, migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: def, Description: t.name})
	}
	return set
}

// ByPath resolves any checkout of a repository to its scope identity.
func (r *RepoRegistry) ByPath(ctx context.Context, rootPath string) (string, error) {
	root, err := scope.CanonicalRepoRoot(ctx, rootPath, r.git)
	if err != nil {
		return "", err
	}
	rec, ok, err := r.graph.RepositoryForRoot(ctx, root)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrRepoNotRegistered
	}
	return rec.ID, nil
}

// ByID requires an existing scope repository identity.
func (r *RepoRegistry) ByID(ctx context.Context, id string) (string, error) {
	rec, ok, err := r.graph.RepositoryByID(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrRepoNotRegistered
	}
	return rec.ID, nil
}

func canonicalGateDir(ctx context.Context, p string, git scope.GitCommonDirFunc) (string, error) {
	d, err := git(ctx, p)
	if err != nil {
		return "", err
	}
	if d.CommonDir == "" {
		return "", cascade.New(cascade.KindInvalidInput, "empty git common dir")
	}
	resolved, err := filepath.EvalSymlinks(d.CommonDir)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "resolve gate common dir")
	}
	return filepath.Clean(resolved), nil
}

// BindGate permanently binds a git common directory to an existing identity.
func (r *RepoRegistry) BindGate(ctx context.Context, gateDir, id string) error {
	if _, err := r.ByID(ctx, id); err != nil {
		return err
	}
	p, err := canonicalGateDir(ctx, gateDir, r.git)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO jobs_repo_gate(gate_path,repo_id,installed_at) VALUES(?,?,?) ON CONFLICT(gate_path) DO NOTHING`, p, id, r.clock.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return registryDBError(err)
	}
	var stored string
	if err := r.db.QueryRowContext(ctx, `SELECT repo_id FROM jobs_repo_gate WHERE gate_path=?`, p).Scan(&stored); err != nil {
		return registryDBError(err)
	}
	if stored != id {
		return cascade.New(cascade.KindConflict, "gate already bound to another repository")
	}
	return nil
}

// GateRepoID returns the identity bound to this git common directory.
func (r *RepoRegistry) GateRepoID(ctx context.Context, gateDir string) (string, error) {
	p, err := canonicalGateDir(ctx, gateDir, r.git)
	if err != nil {
		return "", err
	}
	var id string
	err = r.db.QueryRowContext(ctx, `SELECT repo_id FROM jobs_repo_gate WHERE gate_path=?`, p).Scan(&id)
	if err == sql.ErrNoRows {
		return "", ErrRepoNotRegistered
	}
	return id, registryDBError(err)
}

func registryDBError(err error) error {
	if err == nil {
		return nil
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "repository registry storage")
}
