package repo

// Purpose: the bounded reconciliation sweep against a real git repository.
// SPORT: repo/change-feed/ADD test coverage (P1-GEN-14).

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newSweepEnv is a maintenance env whose fixture is committed, plus its
// Reconciler.
func newSweepEnv(t *testing.T) (*menv, *Reconciler) {
	t.Helper()
	e := newMaintEnv(t)
	commitAll(t, e.root, "fixture")
	r, err := NewReconciler(e.mt, e.mt.d.Journal, e.mt.d.Attention)
	if err != nil {
		t.Fatal(err)
	}
	return e, r
}

// countHashReads counts the files the sweep opens for hashing.
func countHashReads(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var opened []string
	prev := openForHash
	openForHash = func(name string) (io.ReadCloser, error) {
		mu.Lock()
		opened = append(opened, filepath.Base(name))
		mu.Unlock()
		return prev(name)
	}
	t.Cleanup(func() { openForHash = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := opened
		opened = nil
		return out
	}
}

func TestReconcileRepairsDrift(t *testing.T) {
	e, r := newSweepEnv(t)
	ctx := context.Background()
	tracked := strings.Split(git(t, e.root, "ls-files"), "\n")
	reads := countHashReads(t)
	n, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(tracked) || len(reads()) != 0 {
		t.Fatalf("absent marker repaired %d of %d tracked paths", n, len(tracked))
	}
	paths := mustPaths(t, e.mt.d.Journal)
	for _, rel := range tracked {
		if _, ok := paths[rel]; !ok {
			t.Errorf("%s not journaled after the sweep", rel)
		}
	}
	tree := git(t, e.root, "rev-parse", "HEAD^{tree}")
	want := []string{"repo.drift:" + testRepoID + ":" + tree + "|" + strconv.Itoa(len(tracked)) + " paths repaired"}
	if !reflect.DeepEqual(e.attn, want) {
		t.Fatalf("attention = %v, want %v", e.attn, want)
	}
	if cur, ok := mustCursor(t, e.mt.d.Journal); !ok || cur.TreeOID != tree || cur.Commit != git(t, e.root, "rev-parse", "HEAD") {
		t.Fatalf("cursor = %+v, want the tree marker", cur)
	}
	e.idx.Calls = nil
	if n, err := r.Sweep(ctx); err != nil || n != 0 || len(reads()) != 0 || len(e.idx.Calls) != 0 {
		t.Fatalf("current marker, clean tree: repaired %d err %v calls %v", n, err, e.idx.Calls)
	}
	if len(e.attn) != 1 {
		t.Fatalf("a clean sweep raised attention: %v", e.attn)
	}
}

func TestReconcileBoundedWalk(t *testing.T) {
	e, r := newSweepEnv(t)
	ctx := context.Background()
	if _, err := r.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	tracked := strings.Split(git(t, e.root, "ls-files"), "\n")
	e.write(t, "b/b.go", betaChanged)
	if err := os.Remove(filepath.Join(e.root, "c", "c.go")); err != nil {
		t.Fatal(err)
	}
	reads := countHashReads(t)
	cur, _ := mustCursor(t, e.mt.d.Journal)
	gs, err := ReadGitState(ctx, e.root, cur.Commit)
	if err != nil {
		t.Fatal(err)
	}
	drift, err := r.drifted(ctx, gs.Changed, true)
	if err != nil || !reflect.DeepEqual(drift, []string{"b/b.go", "c/c.go"}) {
		t.Fatalf("drifted = %v err %v, want only b/b.go and c/c.go", drift, err)
	}
	if got := len(reads()); got != len(tracked)-1 {
		t.Fatalf("hashed %d files, want every present tracked file (%d)", got, len(tracked)-1)
	}
	e.idx.Calls, e.attn = nil, nil
	n, err := r.Sweep(ctx)
	if err != nil || n != 2 {
		t.Fatalf("repaired %d err %v, want 2", n, err)
	}
	for _, c := range e.idx.Calls {
		if !strings.HasSuffix(c, "b/b.go") && !strings.HasSuffix(c, "c/c.go") && !strings.HasPrefix(c, "graph") && !strings.HasPrefix(c, "del:") {
			t.Errorf("unchanged path re-indexed: %q", c)
		}
	}
	if len(e.attn) != 1 || !strings.HasSuffix(e.attn[0], "|2 paths repaired") {
		t.Fatalf("attention = %v, want one drift item for 2 paths", e.attn)
	}
}

func TestReconcileIntervalConstant(t *testing.T) {
	if ReconcileInterval != 10*time.Minute || DebounceWindow != 500*time.Millisecond || MaxWatches != 4096 {
		t.Fatalf("interval %v debounce %v cap %d", ReconcileInterval, DebounceWindow, MaxWatches)
	}
	e, _ := newSweepEnv(t)
	for _, c := range []struct {
		name    string
		m       *Maintenance
		j       *WatchJournal
		a       Attention
		invalid bool
	}{
		{"maintenance", nil, e.mt.d.Journal, e.mt.d.Attention, true},
		{"journal", e.mt, nil, e.mt.d.Attention, true},
		{"attention", e.mt, e.mt.d.Journal, nil, true},
	} {
		r, err := NewReconciler(c.m, c.j, c.a)
		if r != nil || !isKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: reconciler %v err %v, want KindInvalidInput", c.name, r, err)
		}
	}
	w, sweeps, _ := newTestWatcher(t, e)
	tickSweep := make(chan time.Time)
	w.sweepTick = tickSweep
	stop := runWatcher(t, w)
	waitSweeps(t, sweeps, 1)
	tickSweep <- time.Time{}
	tickSweep <- time.Time{}
	stop()
	if got := sweeps.count(); got != 3 {
		t.Fatalf("sweeps = %d, want start + one per interval tick", got)
	}
}
