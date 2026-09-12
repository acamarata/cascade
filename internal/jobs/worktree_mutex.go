package jobs

// Purpose: HOW step 10's per-repository git-admin-mutation serialization.
//
//	Split from worktree.go purely to keep worktree.go's file under the
//	300-line cap (Art.10.3 — same package, no import-cycle risk, exactly
//	the precedent internal/daemon/subsystems.go vs daemon.go already
//	sets in this same tree). FILES_SCOPE NOTE (recorded, not papered
//	over): this filename is not in the ticket's files_scope add list;
//	see the journal for the full split rationale.
//
// Inputs: a repo root path (runGitAdmin's dir argument) and the
//
//	gitRunner every git-admin call goes through.
//
// Outputs: the mutation's stdout, or a typed A-T7 error — KindCanceled if
//
//	ctx ends mid-backoff, KindUnavailable once the retry ceiling is hit
//	on a persistent index.lock.
//
// Constraints: never a bare time.Sleep (Art.7.3) — the backoff wait goes
//
//	through the injected sleeper seam, exactly as
//	internal/nodes/reconnect.go's own Sleeper does.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// sleeper abstracts the index.lock backoff wait so tests never sit
// through a real delay.
type sleeper interface {
	sleep(ctx context.Context, d time.Duration) bool
}

type realSleeper struct{}

func (realSleeper) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// repoMutexRegistry hands out one *sync.Mutex per canonical repo root, so
// concurrent admitted leases on the SAME repository never race each
// other's `git worktree`/`git write-tree` admin mutations.
type repoMutexRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newRepoMutexRegistry() *repoMutexRegistry {
	return &repoMutexRegistry{locks: make(map[string]*sync.Mutex)}
}

func (r *repoMutexRegistry) forRepo(repoRoot string) *sync.Mutex {
	key := filepath.Clean(repoRoot)
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[key]
	if !ok {
		m = &sync.Mutex{}
		r.locks[key] = m
	}
	return m
}

// The R-21.177 backoff schedule: 50ms base, doubling, capped at 2s, 5
// attempts before refusing with a typed error.
const (
	lockBackoffBase     = 50 * time.Millisecond
	lockBackoffCeiling  = 2 * time.Second
	lockBackoffAttempts = 5
)

// isIndexLockErr reports whether err's wrapped stderr names a git
// index.lock contention, the only failure this backoff schedule retries.
func isIndexLockErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "index.lock")
}

// runGitAdmin runs a git-admin-metadata mutation (add/remove/prune,
// write-tree) in dir, serialized behind repoRoot's mutex with bounded
// backoff on an observed index.lock (HOW step 10).
func (m *WorktreeManager) runGitAdmin(ctx context.Context, repoRoot, dir string, args ...string) (string, error) {
	mu := m.mutexes.forRepo(repoRoot)
	mu.Lock()
	defer mu.Unlock()

	backoff := lockBackoffBase
	var lastErr error
	for attempt := 0; attempt < lockBackoffAttempts; attempt++ {
		out, err := m.git.run(ctx, dir, args...)
		if err == nil {
			return out, nil
		}
		if !isIndexLockErr(err) {
			return out, err
		}
		lastErr = err
		if attempt == lockBackoffAttempts-1 {
			break
		}
		if !m.sleep.sleep(ctx, backoff) {
			return "", cascade.Wrap(cascade.KindCanceled, ctx.Err(), "jobs: git admin mutation canceled during index.lock backoff")
		}
		backoff *= 2
		if backoff > lockBackoffCeiling {
			backoff = lockBackoffCeiling
		}
	}
	return "", cascade.Wrapf(cascade.KindUnavailable, lastErr,
		"jobs: git admin mutation in %s exceeded index.lock backoff ceiling after %d attempts", repoRoot, lockBackoffAttempts)
}
