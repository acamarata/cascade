// Purpose: the daemon's repository maintenance loop. It re-lists the
// registered repositories on an interval, keeps exactly one repo.Watcher
// running per repository, stops the watcher of a repository that is no
// longer registered, and restarts one whose run failed.
// Inputs: cascade.db (inventory, scope, graph, ledger and watch journal),
// the full-text index, the daemon's embedder Selection, the vector and meta
// stores, the injected Clock and an Attention func.
// Outputs: running watchers; an attention item per failed watcher.
// Constraints: a repository's root comes from the scope graph's repo_path
// binding; a repository without one is skipped. Each Watcher sweeps on start
// and every repo.ReconcileInterval, so no second sweep loop runs here.
// SPORT: cmd/cascade repository maintenance composition.

package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// repoWatchRelist is how often the loop re-lists registered repositories.
var repoWatchRelist = repo.ReconcileInterval

// repoWatchEvent observes a watcher's life ("start", "stop"); a test seam.
var repoWatchEvent = func(string, string) {}

// repoWatchEnv is everything one repository's watcher is built from.
type repoWatchEnv struct {
	db        *sql.DB
	idx       *retrieval.Index
	sel       *embedsel.Selection
	vectors   provider.VectorStore
	meta      provider.Store
	clock     runtime.Clock
	attention repo.Attention
	log       *slog.Logger
}

// openRepoWatchDB opens <DataDir>/cascade.db and applies every schema the
// maintenance pipeline reads or writes. The caller closes the handle.
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

// rootOf returns the filesystem root bound to repoID; ok is false when the
// scope graph holds no binding.
func (e *repoWatchEnv) rootOf(ctx context.Context, repoID string) (string, bool, error) {
	var root string
	err := e.db.QueryRowContext(ctx,
		`SELECT root_path FROM context_repo_path WHERE repository_id = ? ORDER BY root_path LIMIT 1`,
		repoID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "repo watch: look up repository root")
	}
	return root, true, nil
}

// newWatcher builds the Watcher for one repository.
func (e *repoWatchEnv) newWatcher(repoID, root string) (*repo.Watcher, error) {
	journal, err := repo.NewWatchJournal(e.db)
	if err != nil {
		return nil, err
	}
	leg, err := newRepoIndexLeg(e.idx, e.sel, e.vectors, e.meta, e.clock)
	if err != nil {
		return nil, err
	}
	m, err := repo.NewMaintenance(repo.MaintenanceDeps{
		Root: root, RepositoryID: repoID, Journal: journal,
		Inventory: repo.NewStore(e.db), Graph: repo.NewGraphStore(e.db), Ledger: repo.NewLedgerStore(e.db),
		Index: leg, Attention: e.attention, Clock: e.clock,
	})
	if err != nil {
		return nil, err
	}
	return repo.NewWatcher(root, m, e.clock)
}

// runningWatch is one started watcher.
type runningWatch struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// repoWatchLoop keeps one watcher per registered repository.
type repoWatchLoop struct {
	env     *repoWatchEnv
	mu      sync.Mutex
	running map[string]*runningWatch
	wg      sync.WaitGroup
}

func newRepoWatchLoop(env *repoWatchEnv) *repoWatchLoop {
	return &repoWatchLoop{env: env, running: map[string]*runningWatch{}}
}

// run lists, reconciles and sleeps until ctx is done, then stops every
// watcher and closes the database handle.
func (l *repoWatchLoop) run(ctx context.Context) error {
	defer func() { _ = l.env.db.Close() }()
	defer l.wg.Wait()
	defer l.stopAll()
	for {
		if err := l.relist(ctx); err != nil && ctx.Err() == nil {
			l.env.log.Warn("repo watch: list repositories", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(repoWatchRelist):
		}
	}
}

// relist starts a watcher for each new row and stops those of removed rows.
func (l *repoWatchLoop) relist(ctx context.Context) error {
	rows, err := repo.NewStore(l.env.db).List(ctx)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(rows))
	for _, inv := range rows {
		want[inv.Repository.ID] = true
		if err := l.ensure(ctx, inv.Repository.ID); err != nil && ctx.Err() == nil {
			l.env.log.Warn("repo watch: start watcher", "repository", inv.Repository.ID, "error", err.Error())
		}
	}
	l.mu.Lock()
	var gone []string
	for id := range l.running {
		if !want[id] {
			gone = append(gone, id)
		}
	}
	l.mu.Unlock()
	for _, id := range gone {
		l.stop(id)
	}
	return nil
}

// ensure starts the watcher for repoID unless one already runs.
func (l *repoWatchLoop) ensure(ctx context.Context, repoID string) error {
	l.mu.Lock()
	_, ok := l.running[repoID]
	l.mu.Unlock()
	if ok {
		return nil
	}
	root, bound, err := l.env.rootOf(ctx, repoID)
	if err != nil || !bound {
		return err
	}
	w, err := l.env.newWatcher(repoID, root)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithCancel(ctx)
	rw := &runningWatch{cancel: cancel, done: make(chan struct{})}
	l.mu.Lock()
	l.running[repoID] = rw
	l.mu.Unlock()
	l.wg.Add(1)
	go l.runWatcher(wctx, repoID, w, rw)
	repoWatchEvent("start", repoID)
	return nil
}

// runWatcher runs w until it stops. A failed run is dropped from the running
// set so the next listing restarts it, and raises an attention item.
func (l *repoWatchLoop) runWatcher(ctx context.Context, repoID string, w *repo.Watcher, rw *runningWatch) {
	defer l.wg.Done()
	defer close(rw.done)
	err := w.Run(ctx)
	repoWatchEvent("stop", repoID)
	if err == nil || ctx.Err() != nil {
		return
	}
	l.mu.Lock()
	if l.running[repoID] == rw {
		delete(l.running, repoID)
	}
	l.mu.Unlock()
	if aerr := l.env.attention(context.WithoutCancel(ctx), "repo.watch:"+repoID, err.Error()); aerr != nil {
		l.env.log.Warn("repo watch: raise attention", "repository", repoID, "error", aerr.Error())
	}
}

// stop cancels the watcher of repoID and waits for it.
func (l *repoWatchLoop) stop(repoID string) {
	l.mu.Lock()
	rw, ok := l.running[repoID]
	delete(l.running, repoID)
	l.mu.Unlock()
	if ok {
		rw.cancel()
		<-rw.done
	}
}

// stopAll stops every running watcher.
func (l *repoWatchLoop) stopAll() {
	l.mu.Lock()
	ids := make([]string, 0, len(l.running))
	for id := range l.running {
		ids = append(ids, id)
	}
	l.mu.Unlock()
	for _, id := range ids {
		l.stop(id)
	}
}
