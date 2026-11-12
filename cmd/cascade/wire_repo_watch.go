//go:build !windows

// Purpose: registers the repo-watch daemon wiring: one supervised loop that
// keeps a repository watcher running for every registered repository.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/localvector"
)

var _ = registerDaemonWiring(daemonRegistration{Name: "repo-watch", Phase: phaseSupervised, Order: 70, Wire: wireRepoWatch})

func wireRepoWatch(w *daemonWiring) error {
	if w.Store == nil || w.Manifest == nil || w.Paths == nil {
		return nil
	}
	idx, err := retrieval.NewIndex(w.Store)
	if err != nil {
		return err
	}
	db, err := openRepoWatchDB(w.Ctx, w.Paths, w.Clock)
	if err != nil {
		return err
	}
	var eff runtime.EffectiveConfig
	if w.Deps.Config != nil {
		eff = runtime.EffectiveConfigOf(w.Deps.Config)
	}
	queue := supervision.NewStore(w.Store, w.Clock, w.Bus, supervision.NewSystemIDGenerator(), 0)
	loop := newRepoWatchLoop(&repoWatchEnv{
		db: db, idx: idx, sel: daemonEmbedderFor(w.Paths, eff), vectors: localvector.New(w.Store),
		meta: w.Store, clock: w.Clock, log: w.Logger, attention: repoAttention(queue),
	})
	w.Manifest.GoSupervised(w.Ctx, "repo-watch", "watching registered repositories", loop.run)
	return nil
}

// repoAttention binds repo.Attention to the supervision queue: one error item
// per source reference, filed under the repository's project scope. The
// second segment of a "<kind>:<repository id>[:...]" reference is the id.
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
