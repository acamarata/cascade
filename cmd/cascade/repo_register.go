// Purpose: the one repository registrar. registerRepository resolves a
// repository's canonical root, makes sure cascade.db carries the scope,
// inventory, graph and ledger schemas, and scans the repository into the
// stored inventory (repo.Scan), so every later fact read or write keys on one
// context_repository id. It is the only production writer of repo.Store.
// Inputs: an absolute repository path, the PathProvider (DataDir holds
// cascade.db) and the injected Clock.
// Outputs: the stored repo.Inventory, or a typed cascade error.
// Constraints: a relative path, or a path git does not place inside a
// repository, is KindInvalidInput; a main checkout, a linked worktree and a
// symlinked path of one repository yield one id (scope.CanonicalRepoRoot);
// the origin remote comes from cascadecontext.GitRemoteURL.
// SPORT: cmd/cascade repository registrar (P1-GEN-02).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// repoRemoteURL is the origin lookup registerRepository records.
var repoRemoteURL repo.RemoteURLFunc = cascadecontext.GitRemoteURL

// repoGitCommonDir resolves repository identity for the registrar.
var repoGitCommonDir scope.GitCommonDirFunc = cascadecontext.GitCommonDir

// unixClock adapts runtime.Clock to repo.Clock (Unix seconds).
type unixClock struct{ c runtime.Clock }

// Now returns the injected clock's instant in Unix seconds.
func (u unixClock) Now() int64 { return u.c.Now().Unix() }

// migrateClock adapts runtime.Clock to migrate.Clock.
type migrateClock struct{ c runtime.Clock }

// Now returns the injected clock's instant.
func (m migrateClock) Now() time.Time { return m.c.Now() }

// canonicalRepoRoot refuses a relative root and resolves it to the canonical
// repository root.
func canonicalRepoRoot(ctx context.Context, root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", cascade.Newf(cascade.KindInvalidInput, "repository path %q must be absolute", root)
	}
	return scope.CanonicalRepoRoot(ctx, filepath.Clean(root), repoGitCommonDir)
}

// withRepoDB opens <DataDir>/cascade.db, applies the four schemas the fact
// ledger rests on, runs fn and closes the handle.
func withRepoDB(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock, fn func(db *sql.DB) error) error {
	if paths == nil || clock == nil {
		return cascade.New(cascade.KindInvalidInput, "repository registry needs paths and a clock")
	}
	dataDir := paths.DataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repository registry: create data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "repository registry: open cascade.db")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	mc, backups := migrateClock{clock}, filepath.Join(dataDir, "backups")
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{
		scope.ApplyScopeSchema, repo.ApplyInventorySchema, repo.ApplyGraphSchema, repo.ApplyLedgerSchema,
	} {
		if err := apply(ctx, db, migrate.SQLiteEmitter{}, mc, dbPath, backups); err != nil {
			return err
		}
	}
	return fn(db)
}

// registerRepository registers root (absolute, inside a git repository) and
// returns its freshly scanned, stored inventory.
func registerRepository(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock, root string) (repo.Inventory, error) {
	canonical, err := canonicalRepoRoot(ctx, root)
	if err != nil {
		return repo.Inventory{}, err
	}
	var inv repo.Inventory
	err = withRepoDB(ctx, paths, clock, func(db *sql.DB) error {
		inv, err = repo.Scan(ctx, repo.ScanDeps{
			Store:        repo.NewStore(db),
			Graph:        scope.NewGraphStore(db),
			GitRoot:      func(context.Context, string) string { return canonical },
			RemoteURL:    repoRemoteURL,
			Clock:        unixClock{clock},
			GitCommonDir: repoGitCommonDir,
		}, canonical)
		return err
	})
	return inv, err
}
