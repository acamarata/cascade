//go:build !windows

// Purpose: shared repository trees and serialized maintenance sweeps.
// Constraints: every content read passes admission; repair quiesces the watcher.
package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// repoWatchRelist bounds discovery of newly registered repositories.
var repoWatchRelist = repo.ReconcileInterval

// repoWatchEvent observes watcher starts and stops in lifecycle tests.
var repoWatchEvent = func(string, string) {}

// repoTree owns one repository's pipeline. Repair holds mu and quiesces watcher.
type repoTree struct {
	mu          sync.Mutex
	id, root    string
	journal     *repo.WatchJournal
	maintenance *repo.Maintenance
	leg         repo.IndexLeg
	watcher     *repo.Watcher
	refused     map[string]string
	cancel      context.CancelFunc
	done        chan struct{}
}

// openRepoWatchDB opens the shared repository database and applies its schemas.
func openRepoWatchDB(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	dataDir := paths.DataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo watch: create data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo watch: open cascade.db")
	}
	db.SetMaxOpenConns(1)
	mc, backups := migrateClock{clock}, filepath.Join(dataDir, "backups")
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{
		scope.ApplyScopeSchema, repo.ApplyInventorySchema, repo.ApplyGraphSchema,
		repo.ApplyLedgerSchema, repo.ApplyWatchJournalSchema,
	} {
		if err := apply(ctx, db, migrate.SQLiteEmitter{}, mc, dbPath, backups); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

// rootOf resolves the persisted binding, without retaining caller paths.
func (s *repoWatchSet) rootOf(ctx context.Context, id string) (string, bool, error) {
	var root string
	err := s.db.QueryRowContext(ctx,
		`SELECT root_path FROM context_repo_path WHERE repository_id = ? ORDER BY root_path LIMIT 1`, id).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "repo watch: look up repository root")
	}
	return root, true, nil
}

// newTree builds the one maintenance writer and starts it quiesced.
func (s *repoWatchSet) newTree(ctx context.Context, id, root string) (*repoTree, error) {
	j, err := repo.NewWatchJournal(s.db)
	if err != nil {
		return nil, err
	}
	m, err := repo.NewMaintenance(repo.MaintenanceDeps{
		Root: root, RepositoryID: id, Journal: j,
		Inventory: repo.NewStore(s.db), Graph: repo.NewGraphStore(s.db), Ledger: repo.NewLedgerStore(s.db),
		Index: s.leg, Attention: s.attention, Clock: s.w.Clock,
	})
	if err != nil {
		return nil, err
	}
	watcher, err := repo.NewWatcher(root, m, s.w.Clock)
	if err != nil {
		return nil, err
	}
	if _, err := watcher.Quiesce(ctx); err != nil {
		return nil, err
	}
	return &repoTree{id: id, root: root, journal: j, maintenance: m, leg: s.leg,
		watcher: watcher, refused: map[string]string{}, done: make(chan struct{})}, nil
}

// sweep locks the tree and waits for any watcher batch before maintenance.
func (t *repoTree) sweep(ctx context.Context) (repo.BatchReport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.watcher.Quiesce(ctx); err != nil {
		t.watcher.Resume()
		return repo.BatchReport{}, err
	}
	defer t.watcher.Resume()
	return t.sweepLocked(ctx)
}

// sweepLocked replays interrupted work and applies only admitted drift.
func (t *repoTree) sweepLocked(ctx context.Context) (repo.BatchReport, error) {
	replay, err := t.maintenance.Replay(ctx)
	if err != nil {
		return replay, err
	}
	prior, err := t.journal.Paths(ctx, t.id)
	if err != nil {
		return replay, err
	}
	paths, err := t.listedPaths(prior)
	if err != nil {
		return replay, err
	}
	drift, err := t.drifted(ctx, paths, prior)
	if err != nil || len(drift) == 0 {
		return replay, err
	}
	cur, _, err := t.journal.Cursor(ctx, t.id)
	if err != nil {
		return replay, err
	}
	return t.maintenance.Apply(ctx, repo.Batch{Changed: drift, FSSeq: cur.FSSeq + 1, Commit: cur.Commit})
}

// listedPaths includes vanished journaled files, so their chunks are retracted.
func (t *repoTree) listedPaths(prior map[string]repo.PathState) ([]string, error) {
	paths := map[string]bool{}
	for rel := range prior {
		paths[rel] = true
	}
	err := filepath.WalkDir(t.root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == t.root {
			return nil
		}
		rel, err := filepath.Rel(t.root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if repo.ExcludedPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			paths[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo watch: list repository paths")
	}
	out := make([]string, 0, len(paths))
	for rel := range paths {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// sweepLogged is the non-request sweep; its report is intentionally ignored.
func (s *repoWatchSet) sweepLogged(ctx context.Context, t *repoTree) {
	if _, err := t.sweep(ctx); err != nil && ctx.Err() == nil {
		s.w.Logger.Warn("repo watch: sweep", "repository", t.id, "error", err.Error())
	}
}
