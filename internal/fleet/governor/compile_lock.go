// Package governor (compile_lock.go) implements the compile-class
// concurrency registry the admission controller enforces its
// one-heavy-compile-per-repo ceiling against (06-FORGE-SPEC.md §5 rule
// 10, R-21.215).
//
// Purpose: CompileLockRegistry counts active compile-class admissions per
//
//	canonical repo path. It carries no resource-sampling or queuing logic
//	of its own; AdmissionController.Admit is the only shipping caller of
//	Lock and Count (tryAdmitLocked), which is what makes Count actually
//	bind rather than sit unread.
//
// Inputs: a repoPath string per call; canonicalized via filepath.Clean so
//
//	"repo", "./repo", and "repo/" all count against the same key.
//
// Outputs: Lock returns an idempotent-per-call unlock func the caller
//
//	invokes exactly once when the compile-class work finishes; Count
//	returns the current active count for a repo path.
//
// Constraints: pure in-process state, no external I/O; thread-safe via a
//
//	single sync.Mutex (contention here is never a bottleneck - calls are
//	only as frequent as Admit/Release, not per-syscall).
//
// SPORT: internal/fleet/governor.CompileLockRegistry (ADD, per T-2
//
//	sport_updates).
package governor

import (
	"path/filepath"
	"sync"
)

// CompileLockRegistry tracks how many compile-class admissions are
// currently active per canonical repo path. The zero value is not usable;
// construct with NewCompileLockRegistry.
type CompileLockRegistry struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewCompileLockRegistry returns an empty CompileLockRegistry.
func NewCompileLockRegistry() *CompileLockRegistry {
	return &CompileLockRegistry{counts: make(map[string]int)}
}

// Lock increments repoPath's active-compile count and returns an unlock
// func that decrements it. unlock is safe to call at most once by
// contract (AdmissionController guards this with sync.Once on the
// Permit); calling it more than once would under-count.
func (r *CompileLockRegistry) Lock(repoPath string) (unlock func()) {
	key := filepath.Clean(repoPath)
	r.mu.Lock()
	r.counts[key]++
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.counts[key] <= 1 {
			delete(r.counts, key)
			return
		}
		r.counts[key]--
	}
}

// Count returns repoPath's current active-compile count. Zero for a repo
// path with no outstanding Lock.
func (r *CompileLockRegistry) Count(repoPath string) int {
	key := filepath.Clean(repoPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[key]
}
