package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"

	_ "modernc.org/sqlite"
)

// ckptTicker is an injected ticker: every send on c is one poll. c is
// unbuffered, so a send returns only once Run is back in its select, which
// means the previous poll has finished.
type ckptTicker struct{ c chan struct{} }

func (t *ckptTicker) C() <-chan struct{} { return t.c }
func (t *ckptTicker) Stop()              {}

func ckptGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=ck@example.invalid", "-c", "user.name=ck", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ckptHead(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	return string(out), err
}

type ckptRig struct {
	t      *testing.T
	store  *Store
	bus    *events.Bus
	lease  ResourceLease
	wt     string
	ticker *ckptTicker
	sub    *events.Subscription
	done   chan error
	cancel context.CancelFunc
}

func newCkptRig(t *testing.T) *ckptRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx := context.Background()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	ckptGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ckptGit(t, repo, "add", ".")
	ckptGit(t, repo, "commit", "-q", "-m", "init")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.SystemClock{}
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	r := &ckptRig{t: t, store: NewStore(db), bus: events.New(storetest.NewMemStore(), clock)}
	tier := provider.SensitivityInternal
	job := Job{ID: "job-1", State: JobStatePending, CreatedAt: 1, UpdatedAt: 1, Capabilities: []string{"code"},
		MutableScope: "**", RiskClass: "low", MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60,
		CostCeiling: 1, Priority: 1, ConsequenceClass: ConsequenceNormal, DataClass: &tier}
	r.lease = ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-1", Epoch: 3, State: LeaseHeld}
	if err := r.store.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := r.store.PutLease(ctx, r.lease); err != nil {
		t.Fatal(err)
	}
	lm := NewLeaseManager(r.store, clock, func() bool { return true }, DefaultLeaseDefaults(), nil)
	wm, err := NewWorktreeManager(r.store, nil, nil, nil, lm.Fence)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wm.Create(ctx, r.lease, repo)
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	r.wt = w.Path
	return r
}

func (r *ckptRig) start() {
	r.t.Helper()
	sub, err := r.bus.Subscribe(context.Background(), leaseEventNamespace, "ckpt-test", 16)
	if err != nil {
		r.t.Fatal(err)
	}
	r.sub = sub
	r.ticker = &ckptTicker{c: make(chan struct{})}
	w, err := NewCheckpointWatcher(r.store, r.bus, ckptHead, r.ticker)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() { r.done <- w.Run(ctx) }()
}

func (r *ckptRig) tick(n int) {
	for range n {
		select {
		case r.ticker.c <- struct{}{}:
		case <-time.After(10 * time.Second):
			r.t.Fatal("watcher did not take a tick")
		}
	}
}

func (r *ckptRig) await(sha string) {
	r.t.Helper()
	for {
		select {
		case ev := <-r.sub.Events:
			if ev.Kind == EventLeaseCheckpoint && decodeCkpt(r.t, ev).CommitSHA == sha {
				return
			}
		case <-time.After(10 * time.Second):
			r.t.Fatalf("no checkpoint event for %s", sha)
		}
	}
}

func decodeCkpt(t *testing.T, ev events.Event) checkpointPayload {
	t.Helper()
	var p checkpointPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	return p
}

func (r *ckptRig) commit(name string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.wt, name), []byte(name+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	ckptGit(r.t, r.wt, "add", ".")
	ckptGit(r.t, r.wt, "commit", "-q", "-m", name)
	return ckptGit(r.t, r.wt, "rev-parse", "HEAD")
}

// TestCheckpointWatcherEmitsOnNewCommit: over a real leased worktree and an
// injected ticker, one EventLeaseCheckpoint per new HEAD sha, none for an
// unchanged HEAD, none after the lease is released.
func TestCheckpointWatcherEmitsOnNewCommit(t *testing.T) {
	r := newCkptRig(t)
	base := ckptGit(t, r.wt, "rev-parse", "HEAD")
	r.start()
	r.tick(1)
	r.await(base)
	r.tick(3) // unchanged HEAD: nothing new
	head := r.commit("a.txt")
	r.tick(1)
	r.await(head)
	r.tick(3)
	released := r.lease
	released.State = LeaseReleased
	if err := r.store.PutLease(context.Background(), released); err != nil {
		t.Fatal(err)
	}
	r.tick(2)
	r.commit("b.txt") // a commit after release must not be reported
	r.tick(3)
	r.cancel()
	if err := <-r.done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	all, err := r.bus.Replay(context.Background(), leaseEventNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []checkpointPayload
	for _, ev := range all {
		if ev.Kind == EventLeaseCheckpoint {
			got = append(got, decodeCkpt(t, ev))
		}
	}
	if len(got) != 2 || got[0].CommitSHA != base || got[1].CommitSHA != head {
		t.Fatalf("checkpoints = %+v, want exactly [%s %s]", got, base, head)
	}
	p := got[1]
	if p.JobID != "job-1" || p.RepoID != r.lease.RepoID || p.ScopeGlob != "**" || p.Epoch != 3 ||
		p.LeaseID != LeaseID(r.lease) || p.ObservedAt == 0 {
		t.Fatalf("payload = %+v, want job-1 / %s / ** / epoch 3 / lease id / observed_at", p, r.lease.RepoID)
	}
}

func TestNewCheckpointWatcherNamesMissingInput(t *testing.T) {
	_, err := NewCheckpointWatcher(nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("err = %v, want a missing-store refusal", err)
	}
}

// runWithGit starts a watcher over r's store with git and returns its
// ticker and Run's result channel.
func runWithGit(t *testing.T, r *ckptRig, git func(context.Context, string) (string, error)) (*ckptTicker, chan error, context.CancelFunc) {
	t.Helper()
	tk := &ckptTicker{c: make(chan struct{})}
	w, err := NewCheckpointWatcher(r.store, r.bus, git, tk)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return tk, done, cancel
}

// TestCheckpointWatcherFailsLoudOnBrokenWorktree: git failures on a
// worktree that still exists, for a lease still held, end Run on exactly the
// maxGitFailures-th consecutive tick, with the git error.
func TestCheckpointWatcherFailsLoudOnBrokenWorktree(t *testing.T) {
	r := newCkptRig(t)
	tk, done, _ := runWithGit(t, r, func(context.Context, string) (string, error) {
		return "", errors.New("git: broken worktree")
	})
	sent := 0
	for {
		select {
		case tk.c <- struct{}{}:
			sent++
			continue
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "git: broken worktree") || sent != maxGitFailures {
				t.Fatalf("Run = %v after %d ticks, want the git failure after %d", err, sent, maxGitFailures)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Run still polling after %d failing ticks", sent)
		}
		return
	}
}

// TestCheckpointWatcherSkipsLeaseReleasedMidPoll: a git failure on a lease
// released after the tick listed it (its worktree is being removed) is
// skipped; Run keeps polling and stops cleanly.
func TestCheckpointWatcherSkipsLeaseReleasedMidPoll(t *testing.T) {
	r := newCkptRig(t)
	released := r.lease
	released.State = LeaseReleased
	tk, done, cancel := runWithGit(t, r, func(ctx context.Context, _ string) (string, error) {
		if err := r.store.PutLease(ctx, released); err != nil {
			return "", err
		}
		return "", errors.New("git: not a git repository (removal in progress)")
	})
	for i := 0; i < maxGitFailures+2; i++ {
		select {
		case tk.c <- struct{}{}:
		case err := <-done:
			t.Fatalf("Run ended after %d ticks: %v; want the released lease skipped", i, err)
		case <-time.After(10 * time.Second):
			t.Fatal("watcher did not take a tick")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want a clean stop", err)
	}
}
