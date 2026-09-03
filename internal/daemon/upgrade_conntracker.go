//go:build !windows

package daemon

// Purpose: ConnTracker, Drain's in-flight-work registry. Split out of
// upgrade.go (which owns UpgradeManager itself) purely to stay under the
// 300-line file cap; this is a files_scope deviation the ticket's own
// files_scope did not list, made because a hard-gate violation (a file
// over the cap fails the build lint outright) is worse than one small,
// tightly-scoped extra file holding a single self-contained type. See the
// ticket report for the full note.
//
// Inputs: an io.Closer per Begin/End pair (typically one accepted
// connection).
//
// Outputs: a Done() channel Drain polls; CloseAll()'s force-closed count.
//
// Constraints: safe for concurrent use; no clock, no I/O beyond Close.
//
// SPORT: internal/daemon (ADD, part of the DaemonUpgradeManager entity).

import (
	"io"
	"sync"
)

// ConnTracker lets a caller register in-flight work with Drain so it can
// wait for graceful completion and force-close stragglers past grace.
// Safe for concurrent use.
type ConnTracker struct {
	mu    sync.Mutex
	wg    sync.WaitGroup
	conns map[io.Closer]struct{}
}

// NewConnTracker returns a ready, empty ConnTracker.
func NewConnTracker() *ConnTracker {
	return &ConnTracker{conns: make(map[io.Closer]struct{})}
}

// Begin registers one in-flight unit of work (typically one accepted
// connection's handler goroutine). c may be nil when a caller only needs
// the WaitGroup half: a mock "in-flight handler" with nothing to
// force-close, as this ticket's tests use.
func (t *ConnTracker) Begin(c io.Closer) {
	t.wg.Add(1)
	if c == nil {
		return
	}
	t.mu.Lock()
	t.conns[c] = struct{}{}
	t.mu.Unlock()
}

// End marks one unit of work done. Must be called exactly once per
// matching Begin, typically via defer.
func (t *ConnTracker) End(c io.Closer) {
	if c != nil {
		t.mu.Lock()
		delete(t.conns, c)
		t.mu.Unlock()
	}
	t.wg.Done()
}

// Done returns a channel closed once every Begin has a matching End.
func (t *ConnTracker) Done() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	return done
}

// CloseAll force-closes every still-registered connection and reports how
// many it closed.
func (t *ConnTracker) CloseAll() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for c := range t.conns {
		_ = c.Close()
		delete(t.conns, c)
		n++
	}
	return n
}
