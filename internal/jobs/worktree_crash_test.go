package jobs

// Purpose: C16 per-site crash injection for WorktreeManager.Create. A
//
//	child copy of this test binary runs Create with a git stand-in
//	(testdata/git-crash-standins) that SIGKILLs it just before or just
//	after the real `git worktree add`. The parent then runs Sweep and
//	Create with the real git and proves the job worktrees git lists equal
//	the stored rows exactly. TestWorktreePendingRowReaders covers the
//	pending-row readers: Remove runs no git, Snapshot refuses NotFound.
//	recordingGit (shared with worktree_fence_test.go) logs every git
//	invocation so a test can prove zero git commands ran.
//
// SPORT: jobs/worktree-manager (P1-CORE-06).

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const crashChildEnv = "CASCADE_WT_CRASH_CHILD"

// recordingGit returns a git stand-in that logs every invocation to the
// returned file and then execs the real git. A positive control proves the
// log is written, so an empty log later really means zero invocations. On
// windows the POSIX stand-in cannot run: bin is the real git and record is
// "", so only requireNoGitCalls is skipped and every other assertion runs.
func recordingGit(t *testing.T) (bin, record string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return "", ""
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath git: %v", err)
	}
	bin, err = filepath.Abs(filepath.Join("testdata", "git-crash-standins", "record-git.sh"))
	if err != nil {
		t.Fatalf("stand-in path: %v", err)
	}
	record = filepath.Join(t.TempDir(), "git-invocations.log")
	t.Setenv("CASCADE_REAL_GIT", realGit)
	t.Setenv("CASCADE_GIT_RECORD", record)
	if out, err := exec.Command(bin, "--version").CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Fatalf("recording stand-in control run: %v %s", err, out)
	}
	if got := readRecord(t, record); len(got) != 1 {
		t.Fatalf("recording stand-in control logged %d lines, want 1", len(got))
	}
	if err := os.Remove(record); err != nil {
		t.Fatalf("reset record: %v", err)
	}
	return bin, record
}

// requireNoGitCalls asserts record logged zero git invocations. It is the
// one assertion that needs the POSIX recording stand-in, so it alone is
// skipped on windows (record == "").
func requireNoGitCalls(t *testing.T, record, what string) {
	t.Helper()
	if record == "" {
		t.Log(what + ": zero-git-call assertion skipped on windows; the recording stand-in is a POSIX shell script")
		return
	}
	if got := readRecord(t, record); len(got) != 0 {
		t.Fatalf("%s ran git %d times: %v", what, len(got), got)
	}
}

func readRecord(t *testing.T, record string) []string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// openCrashStore opens (and migrates) the real sqlite file at dbPath.
func openCrashStore(t *testing.T, dbPath string) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	return NewStore(db)
}

// runCrashChild is the child half: Create under the crash stand-in. It
// must never return normally; reaching the end means the stand-in did not
// kill this process.
func runCrashChild(t *testing.T) {
	store := openCrashStore(t, os.Getenv("CASCADE_WT_DB"))
	repo := os.Getenv("CASCADE_WT_REPO")
	lease, ok, err := store.GetLease(context.Background(), repo, "**")
	if err != nil || !ok {
		t.Fatalf("child GetLease: ok=%v err=%v", ok, err)
	}
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	_, err = wm.withGitBinary(os.Getenv("CASCADE_WT_STANDIN")).Create(context.Background(), lease, repo)
	t.Fatalf("child Create returned (err=%v) instead of being killed", err)
}

// crashCreate runs the named test in a child process with standin as its
// git and asserts the child died by signal. It returns the parent's store.
func crashCreate(t *testing.T, standin string) (*Store, ResourceLease, string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip(t.Name() + ": crash stand-ins are POSIX shell scripts that kill -9 their parent; not run on windows")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath git: %v", err)
	}
	bin, err := filepath.Abs(filepath.Join("testdata", "git-crash-standins", standin))
	if err != nil {
		t.Fatalf("stand-in path: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "crash.db")
	store := openCrashStore(t, dbPath)
	repo := newTestGitRepo(t)
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-crash", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashChildEnv+"=1", "CASCADE_WT_DB="+dbPath, "CASCADE_WT_REPO="+repo,
		"CASCADE_WT_STANDIN="+bin, "CASCADE_REAL_GIT="+realGit)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != -1 {
		t.Fatalf("child was not killed by a signal: err=%v\n%s", err, out)
	}
	return store, lease, repo
}

// jobWorktreeSets returns the canonical job worktree paths git lists under
// repo's worktrees directory, and the canonical paths of the active rows.
func jobWorktreeSets(t *testing.T, wm *WorktreeManager, store *Store, repo string) (listed, rows []string) {
	t.Helper()
	entries, err := wm.listGitWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("listGitWorktrees: %v", err)
	}
	jobsDir := canonicalPath(filepath.Join(repo, filepath.FromSlash(worktreesDirName)))
	for _, e := range entries {
		if p := canonicalPath(e.Path); filepath.Dir(p) == jobsDir {
			listed = append(listed, p)
		}
	}
	active, err := listActiveWorktreeRows(context.Background(), store)
	if err != nil {
		t.Fatalf("listActiveWorktreeRows: %v", err)
	}
	for _, w := range active {
		rows = append(rows, canonicalPath(w.Path))
	}
	sort.Strings(listed)
	sort.Strings(rows)
	return listed, rows
}

// recoverAfterCrash runs the parent's Sweep then Create with the real git
// and asserts git's job worktrees equal the rows exactly.
func recoverAfterCrash(t *testing.T, store *Store, lease ResourceLease, repo string) {
	t.Helper()
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	if _, err := wm.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep after crash: %v", err)
	}
	w, err := wm.Create(context.Background(), lease, repo)
	if err != nil || w.Path != jobWorktreeDir(repo, lease.Holder) {
		t.Fatalf("Create after crash = %+v, %v", w, err)
	}
	listed, rows := jobWorktreeSets(t, wm, store, repo)
	if len(listed) != 1 || len(rows) != 1 || listed[0] != rows[0] {
		t.Fatalf("git job worktrees %v != rows %v (want exactly one, equal)", listed, rows)
	}
}

func TestWorktreeCreateCrashAfterIntent(t *testing.T) {
	if os.Getenv(crashChildEnv) != "" {
		runCrashChild(t)
		return
	}
	store, lease, repo := crashCreate(t, "kill-before-add.sh")
	path := jobWorktreeDir(repo, lease.Holder)
	if _, ok, err := store.GetWorktree(context.Background(), path); err != nil || !ok {
		t.Fatalf("intent row missing after crash before add: ok=%v err=%v", ok, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("worktree exists although the crash came before the add: %v", statErr)
	}
	recoverAfterCrash(t, store, lease, repo)
}

func TestWorktreeCreateCrashAfterAdd(t *testing.T) {
	if os.Getenv(crashChildEnv) != "" {
		runCrashChild(t)
		return
	}
	store, lease, repo := crashCreate(t, "kill-after-add.sh")
	path := jobWorktreeDir(repo, lease.Holder)
	if _, ok, err := store.GetWorktree(context.Background(), path); err != nil || !ok {
		t.Fatalf("intent row missing after crash after add: ok=%v err=%v", ok, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("worktree missing although the crash came after the add: %v", statErr)
	}
	recoverAfterCrash(t, store, lease, repo)
}

func TestWorktreePendingRowReaders(t *testing.T) {
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-pending", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	pending := Worktree{Path: jobWorktreeDir(repo, lease.Holder), LeaseRepoID: repo, LeaseScopeGlob: "**", Repo: repo, Branch: jobBranch(lease.Holder)}
	if err := store.PutWorktree(ctx, pending); err != nil {
		t.Fatalf("PutWorktree (intent row): %v", err)
	}

	_, err := wm.Snapshot(ctx, nil, lease, lease.Epoch, nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("Snapshot on a pending row: err = %v, want KindNotFound", err)
	}
	bin, record := recordingGit(t)
	if err := wm.withGitBinary(bin).Remove(ctx, lease); err != nil {
		t.Fatalf("Remove on a pending row: %v", err)
	}
	requireNoGitCalls(t, record, "Remove on a pending row")
	if _, ok, err := store.GetWorktree(ctx, pending.Path); err != nil || ok {
		t.Fatalf("pending row survived Remove: ok=%v err=%v", ok, err)
	}
}

// TestWorktreeRemoveIgnoresPreviousHolder: a previous holder's release
// (Remove with holder a's lease) after holder b owns the lease key and a
// clean tree must leave b's tree, row and git state untouched.
func TestWorktreeRemoveIgnoresPreviousHolder(t *testing.T) {
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	leaseB := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "b", Epoch: 2, State: LeaseHeld}
	mustPutLease(t, store, leaseB)
	wB, err := wm.Create(ctx, leaseB, repo)
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	before := gitState(t, repo)
	leaseA := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "a", Epoch: 1, State: LeaseReleased}
	if err := wm.Remove(ctx, leaseA); err != nil {
		t.Fatalf("Remove(previous holder a): %v", err)
	}
	if _, statErr := os.Stat(wB.Path); statErr != nil {
		t.Fatalf("holder b's tree removed by holder a's Remove: %v", statErr)
	}
	if row, ok, err := store.GetWorktree(ctx, wB.Path); err != nil || !ok || row != wB {
		t.Fatalf("holder b's row after a's Remove = %+v ok=%v err=%v, want %+v", row, ok, err, wB)
	}
	if after := gitState(t, repo); after != before {
		t.Fatalf("git state changed by a's Remove:\nbefore %q\nafter  %q", before, after)
	}
}
