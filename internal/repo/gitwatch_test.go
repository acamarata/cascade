package repo

// Purpose: ReadGitState against repositories built with the real git binary.
// SPORT: repo/change-feed/ADD test coverage (P1-GEN-14).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// git runs the real git binary in root with a fixed test identity.
func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", root, "-c", "user.name=watch-test", "-c", "user.email=watch@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newGitRepo initialises an empty repository under t.TempDir().
func newGitRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	return root
}

func writeRel(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, full, content, 0o644)
}

func commitAll(t *testing.T, root, msg string) string {
	t.Helper()
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", msg)
	return git(t, root, "rev-parse", "HEAD")
}

func TestGitWatchPorcelainKinds(t *testing.T) {
	root := newGitRepo(t)
	ctx := context.Background()
	empty, err := ReadGitState(ctx, root, "")
	if err != nil || empty.Head != "" || empty.TreeOID != "" || len(empty.Changed) != 0 {
		t.Fatalf("empty repo state = %+v err %v", empty, err)
	}
	if _, err := ReadGitState(ctx, root, "0123456789abcdef0123456789abcdef01234567"); err != ErrGap { //nolint:errorlint // identity
		t.Fatalf("since on a repo without HEAD = %v, want ErrGap", err)
	}
	for _, f := range []string{"keep.txt", "mod.txt", "old.txt", "gone.txt"} {
		writeRel(t, root, f, f+"\n")
	}
	head := commitAll(t, root, "base")
	writeRel(t, root, "mod.txt", "unstaged edit\n")
	writeRel(t, root, "staged.txt", "staged\n")
	git(t, root, "add", "staged.txt")
	git(t, root, "mv", "old.txt", "new.txt")
	git(t, root, "rm", "-q", "gone.txt")
	writeRel(t, root, "nested/untracked.txt", "untracked\n")
	gs, err := ReadGitState(ctx, root, head)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gone.txt", "mod.txt", "nested/untracked.txt", "new.txt", "old.txt", "staged.txt"}
	if !reflect.DeepEqual(gs.Changed, want) {
		t.Fatalf("changed = %v, want %v", gs.Changed, want)
	}
	if gs.Head != head || gs.TreeOID != git(t, root, "rev-parse", "HEAD^{tree}") || len(gs.TreeOID) != 40 {
		t.Fatalf("head %q tree %q, want HEAD %q and its tree", gs.Head, gs.TreeOID, head)
	}
	if _, err := ReadGitState(ctx, filepath.Join(root, "missing"), ""); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("not a repository = %v, want KindUnavailable", err)
	}
}

func isKind(err error, k cascade.Kind) bool {
	var ce *cascade.Error
	return errors.As(err, &ce) && ce.Kind == k
}

func TestGitWatchBranchSwitch(t *testing.T) {
	root := newGitRepo(t)
	ctx := context.Background()
	for i := 0; i < 250; i++ {
		writeRel(t, root, fmt.Sprintf("d%d/f%03d.txt", i%5, i), "base\n")
	}
	base := commitAll(t, root, "base")
	git(t, root, "checkout", "-q", "-b", "feature")
	var want []string
	for i := 0; i < 200; i++ {
		rel := fmt.Sprintf("d%d/f%03d.txt", i%5, i)
		writeRel(t, root, rel, "feature\n")
		want = append(want, rel)
	}
	commitAll(t, root, "rewrite 200")
	git(t, root, "checkout", "-q", "main")
	if gs, err := ReadGitState(ctx, root, base); err != nil || gs.Head != base || len(gs.Changed) != 0 {
		t.Fatalf("on base: %+v err %v", gs, err)
	}
	git(t, root, "checkout", "-q", "feature")
	gs, err := ReadGitState(ctx, root, base)
	if err != nil {
		t.Fatal(err)
	}
	want = normalizeRels(want)
	if !reflect.DeepEqual(gs.Changed, want) {
		t.Fatalf("branch switch changed %d paths, want exactly the 200 rewritten", len(gs.Changed))
	}
	git(t, root, "checkout", "-q", "--orphan", "other")
	writeRel(t, root, "solo.txt", "x\n")
	git(t, root, "rm", "-rq", "--cached", ".")
	git(t, root, "add", "solo.txt")
	git(t, root, "commit", "-q", "-m", "orphan")
	_, err = ReadGitState(ctx, root, base)
	if err != ErrGap || !strings.HasSuffix(err.Error(), "repo: watch cursor cannot advance monotonically") { //nolint:errorlint // identity
		t.Fatalf("non-descendant cursor = %v, want ErrGap", err)
	}
}

// The tests below drive the watcher over real fsnotify on a scratch tree.

// realWatcher is a test watcher over root using the real fsnotify source;
// watched records every directory watch.
func realWatcher(t *testing.T, root string) (*testWatcher, *counter, chan string) {
	t.Helper()
	e := newWatchEnv(t)
	w, sweeps, _ := newTestWatcher(t, e)
	watched := make(chan string, 256)
	w.root, w.open = root, openFSNotify
	w.onWatch = func(dir string) { watched <- dir }
	return w, sweeps, watched
}

func drain(ch chan string) map[string]bool {
	got := map[string]bool{}
	for {
		select {
		case d := <-ch:
			got[d] = true
		default:
			return got
		}
	}
}

func TestWatchSkipsExcludedDirs(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join("testdata", "fixture-watch")
	for _, rel := range []string{"src/main.txt", "src/deep/leaf.txt", "docs/notes.md"} {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		writeRel(t, root, rel, string(data))
	}
	mkdirs(t, root, ".git/objects", "node_modules/pkg", ".hidden/x", "build/out")
	w, sweeps, watched := realWatcher(t, root)
	stop := runWatcher(t, w)
	defer stop()
	waitSweeps(t, sweeps, 1)
	want := map[string]bool{root: true, filepath.Join(root, "src"): true, filepath.Join(root, "src", "deep"): true,
		filepath.Join(root, "docs"): true}
	if got := drain(watched); !reflect.DeepEqual(got, want) {
		t.Fatalf("watched %v, want directories only, skipping .git and excluded segments: %v", got, want)
	}
	mkdirs(t, root, "fresh/inner", "vendor/lib")
	seen := map[string]bool{}
	for !seen[filepath.Join(root, "fresh", "inner")] {
		select {
		case d := <-watched:
			if d != filepath.Join(root, "fresh") && d != filepath.Join(root, "fresh", "inner") {
				t.Fatalf("unexpected watch %s", d)
			}
			seen[d] = true
		case <-time.After(30 * time.Second):
			t.Fatalf("a created directory was not followed: %v", seen)
		}
	}
}

func TestWatchCapFallsBackToSweep(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b", "c", "d")
	e := newWatchEnv(t)
	w, sweeps, _ := newTestWatcher(t, e)
	w.root, w.open, w.maxWatches = root, openFSNotify, 2
	sweepTick := make(chan time.Time)
	w.sweepTick = sweepTick
	stop := runWatcher(t, w)
	waitSweeps(t, sweeps, 1)
	sweepTick <- time.Time{}
	sweepTick <- time.Time{}
	stop()
	if len(e.attn) != 1 || !strings.HasPrefix(e.attn[0], "repo.watch-capped:"+testRepoID+"|") {
		t.Fatalf("attention = %v, want one watch-capped item", e.attn)
	}
	if sweeps.count() != 3 || len(w.watched) != 0 {
		t.Fatalf("sweeps %d watches %d, want sweep-only with no watches", sweeps.count(), len(w.watched))
	}
}

// openFDs counts this process's open descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if ents, err := os.ReadDir(dir); err == nil {
			return len(ents)
		}
	}
	return probeFDs()
}

// probeFDs counts open descriptors where no descriptor directory is readable
// (sandboxes deny /dev/fd): a zero-offset relative seek is side-effect free and
// fails with EBADF only on a closed descriptor. syscall.Seek takes an int on
// unix and a Handle on windows, so the argument is converted by reflection.
func probeFDs() int {
	seek := reflect.ValueOf(syscall.Seek)
	n := 0
	for fd := 0; fd < 8192; fd++ {
		arg := reflect.ValueOf(fd).Convert(seek.Type().In(0))
		out := seek.Call([]reflect.Value{arg, reflect.ValueOf(int64(0)), reflect.ValueOf(1)})
		if err, _ := out[1].Interface().(error); !errors.Is(err, syscall.EBADF) {
			n++
		}
	}
	return n
}

func TestWatcherClosesOnCancel(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows has no descriptor directory")
	}
	root := t.TempDir()
	mkdirs(t, root, "a/b/c", "d/e", "f")
	e := newWatchEnv(t)
	run := func(check func()) {
		w, sweeps, _ := newTestWatcher(t, e)
		w.root, w.open = root, openFSNotify
		stop := runWatcher(t, w)
		waitSweeps(t, sweeps, 1)
		check()
		stop()
	}
	run(func() {}) // warm the runtime's own descriptors
	base := openFDs(t)
	run(func() {
		if open := openFDs(t); open <= base {
			t.Fatalf("running watcher holds %d descriptors, baseline %d: watches not counted", open, base)
		}
	})
	got := openFDs(t)
	for deadline := time.After(10 * time.Second); got > base; got = openFDs(t) {
		goruntime.GC() // descriptors other tests left to finalizers
		select {
		case <-deadline:
			t.Fatalf("descriptors after cancel = %d, want at most the baseline %d", got, base)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
