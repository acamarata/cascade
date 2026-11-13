//go:build !windows

// Purpose: supervise repository watchers and serve repairs in their owning loop.
package main

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/providers/localvector"
)

var _ = registerDaemonWiring(daemonRegistration{Name: "repo-watch", Phase: phaseSupervised, Order: 70, Wire: wireRepoWatch})

// repoWatchSet.live belongs exclusively to run; callers use requests.
type repoWatchSet struct {
	w         *daemonWiring
	db        *sql.DB
	leg       repo.IndexLeg
	attention repo.Attention
	live      map[string]*repoTree
	requests  chan repoRepairRequest
	stopped   chan struct{}
}

func wireRepoWatch(w *daemonWiring) error {
	if w.Store == nil || w.Manifest == nil || w.Paths == nil || w.Deps.RawDB == nil {
		mountContextRepair(w, nil)
		return nil
	}
	idx, err := retrieval.NewIndex(w.Store)
	if err != nil {
		return err
	}
	leg, err := newRepoIndexLeg(idx, processEmbedder(w.Paths), localvector.New(w.Store), w.Store, w.Clock)
	if err != nil {
		return err
	}
	db, err := openRepoWatchDB(w.Ctx, w.Paths, w.Clock)
	if err != nil {
		return err
	}
	queue := supervision.NewStore(w.Store, w.Clock, w.Bus, supervision.NewSystemIDGenerator(), 0)
	set := &repoWatchSet{w: w, db: db, leg: leg, attention: repoAttention(queue),
		live: map[string]*repoTree{}, requests: make(chan repoRepairRequest), stopped: make(chan struct{})}
	mountContextRepair(w, set)
	w.Manifest.GoSupervised(w.Ctx, "repo-watch", "watching registered repositories", set.run)
	return nil
}

// run is the only goroutine that reads or writes live. Its context owns repairs.
func (s *repoWatchSet) run(ctx context.Context) error {
	defer close(s.stopped)
	defer func() { _ = s.db.Close() }()
	defer s.stopAll()
	ticker := time.NewTicker(repoWatchRelist)
	defer ticker.Stop()
	s.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-s.requests:
			s.serveRepair(ctx, req)
		case <-ticker.C:
			s.reconcile(ctx)
		}
	}
}

// reconcile discovers new registrations and joins removed or failed watchers.
func (s *repoWatchSet) reconcile(ctx context.Context) {
	rows, err := repo.NewStore(s.db).List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.w.Logger.Warn("repo watch: list repositories", "error", err.Error())
		}
		return
	}
	want := map[string]bool{}
	for _, inv := range rows {
		id := inv.Repository.ID
		want[id] = true
		if err := s.start(ctx, id, false); err != nil && ctx.Err() == nil {
			s.w.Logger.Warn("repo watch: start watcher", "repository", id, "error", err.Error())
		}
	}
	for id := range s.live {
		if !want[id] {
			s.stop(id)
		}
	}
}

// start constructs and supervises a tree. A request performs its own first sweep.
func (s *repoWatchSet) start(ctx context.Context, id string, requested bool) error {
	if tree := s.live[id]; tree != nil {
		select {
		case <-tree.done:
			s.stop(id)
		default:
			return nil
		}
	}
	root, bound, err := s.rootOf(ctx, id)
	if err != nil || !bound {
		return err
	}
	tree, err := s.newTree(ctx, id, root)
	if err != nil {
		return err
	}
	if !requested {
		s.sweepLogged(ctx, tree)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	tree.cancel = cancel
	s.live[id] = tree
	repoWatchEvent("start", id)
	s.w.Manifest.GoSupervised(watchCtx, "repo-watch:"+id, "watching repository", func(ctx context.Context) error {
		defer close(tree.done)
		defer repoWatchEvent("stop", id)
		err := tree.watcher.Run(ctx)
		if err != nil && ctx.Err() == nil {
			if aerr := s.attention(ctx, "repo.watch:"+id, err.Error()); aerr != nil {
				s.w.Logger.Warn("repo watch: raise attention", "repository", id, "error", aerr.Error())
			}
		}
		return err
	})
	return nil
}

// stop cancels and joins one child before removing its tree.
func (s *repoWatchSet) stop(id string) {
	tree := s.live[id]
	if tree == nil {
		return
	}
	tree.cancel()
	<-tree.done
	delete(s.live, id)
}

// stopAll joins children before the loop closes its database.
func (s *repoWatchSet) stopAll() {
	for id := range s.live {
		s.stop(id)
	}
}

// repoAttention binds repository failures to the supervision queue.
func repoAttention(q *supervision.Store) repo.Attention {
	return func(ctx context.Context, sourceRef, _ string) error {
		id := sourceRef
		if parts := strings.SplitN(sourceRef, ":", 3); len(parts) > 1 {
			id = parts[1]
		}
		_, err := q.Push(ctx, supervision.AttentionItem{
			Kind: supervision.KindError, SourceRef: sourceRef,
			ScopeRef: supervision.ScopeRef{Kind: scope.ScopeKindProject, ID: id}, Priority: 1,
		})
		return err
	}
}
