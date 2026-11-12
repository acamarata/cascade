//go:build !windows

package main

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// watchLog records the watcher start and stop events.
type watchLog struct {
	mu     sync.Mutex
	starts map[string]int
	stops  map[string]int
}

func (l *watchLog) record(kind, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if kind == "start" {
		l.starts[id]++
	} else {
		l.stops[id]++
	}
}

func (l *watchLog) counts() (starts, stops map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	starts, stops = map[string]int{}, map[string]int{}
	for k, v := range l.starts {
		starts[k] = v
	}
	for k, v := range l.stops {
		stops[k] = v
	}
	return starts, stops
}

// composeWatchDaemon composes the daemon with a fast re-list interval and
// returns the event log, the daemon's paths and a stop func that runs the
// daemon's cleanups once.
func composeWatchDaemon(t *testing.T) (*watchLog, runtime.PathProvider, func()) {
	t.Helper()
	if !gitRunnable() {
		factsFixtureGit(t)
	}
	t.Setenv("HOME", t.TempDir())
	log := &watchLog{starts: map[string]int{}, stops: map[string]int{}}
	oldEvent, oldRelist := repoWatchEvent, repoWatchRelist
	repoWatchEvent, repoWatchRelist = log.record, 20*time.Millisecond
	t.Cleanup(func() { repoWatchEvent, repoWatchRelist = oldEvent, oldRelist })
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	_, cleanups, err := composeDaemon(context.Background(), deps, func(*rpc.Registry, *rpc.SSEMux) {})
	if err != nil {
		runCleanupsLIFO(cleanups)
		t.Fatalf("composeDaemon: %v", err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { runCleanupsLIFO(cleanups) }) }
	t.Cleanup(stop)
	return log, deps.Paths, stop
}

func registerForWatch(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	dir := factsGitRepo(t, map[string]string{"a.go": "package a\n"})
	inv, err := registerRepository(context.Background(), paths, runtime.SystemClock{}, dir)
	if err != nil {
		t.Fatalf("registerRepository: %v", err)
	}
	return inv.Repository.ID
}

func waitStarted(t *testing.T, log *watchLog, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		starts, _ := log.counts()
		ok := true
		for _, id := range ids {
			ok = ok && starts[id] > 0
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	starts, _ := log.counts()
	t.Fatalf("no watcher started for %v; starts %v", ids, starts)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDaemonWatcherLifecycle(t *testing.T) {
	log, paths, stop := composeWatchDaemon(t)
	a, b := registerForWatch(t, paths), registerForWatch(t, paths)
	waitStarted(t, log, a, b)
	time.Sleep(200 * time.Millisecond) // several re-list rounds
	starts, _ := log.counts()
	if len(starts) != 2 || starts[a] != 1 || starts[b] != 1 {
		t.Fatalf("want exactly one watcher per repository, got %v", starts)
	}
	stop()
	_, stops := log.counts()
	if stops[a] != 1 || stops[b] != 1 {
		t.Fatalf("daemon stop must stop every watcher once, got %v", stops)
	}
}

func TestDaemonWatchesRepoRegisteredLater(t *testing.T) {
	log, paths, _ := composeWatchDaemon(t)
	first := registerForWatch(t, paths)
	waitStarted(t, log, first)
	later := registerForWatch(t, paths)
	waitStarted(t, log, later)
	starts, _ := log.counts()
	if got := sortedKeys(starts); len(got) != 2 {
		t.Fatalf("watched repositories = %v, want the first and the later one", got)
	}
}
