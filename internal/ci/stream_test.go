// Purpose: the streaming dispatcher's shared test rig and its entry-point
// tests: a REAL git repository with a REAL jobs stack (store, lease
// manager, worktree manager, outbox) under t.TempDir() with HOME and
// USERPROFILE redirected there. Only the SubJobExecutor and the
// AttentionPusher are scripted; the code under test, the fence, the
// snapshot and the outbox are the real implementations.
//
// SPORT: internal.ci.Dispatcher/TESTED (P1-CI-01).
package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// recExec is a scripted SubJobExecutor: it records every SubJob and hands
// out distinct negative run ids.
type recExec struct {
	mu   sync.Mutex
	runs []SubJob
	fn   func(SubJob) (SubJobResult, error)
}

func (r *recExec) Run(_ context.Context, sj SubJob) (SubJobResult, error) {
	r.mu.Lock()
	r.runs = append(r.runs, sj)
	id := -int64(len(r.runs))
	fn := r.fn
	r.mu.Unlock()
	if fn != nil {
		return fn(sj)
	}
	return SubJobResult{RunID: id, RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
}

func (r *recExec) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, sj := range r.runs {
		out = append(out, string(sj.Kind))
	}
	sort.Strings(out)
	return out
}

func (r *recExec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// streamRig is one job's complete fixture.
type streamRig struct {
	t      *testing.T
	repo   string
	wt     string
	base   string
	ciDB   *sql.DB
	jobsDB *sql.DB
	store  *jobs.Store
	lm     *jobs.LeaseManager
	wm     *jobs.WorktreeManager
	lease  jobs.ResourceLease
	bus    *events.Bus
	attn   *fakePusher
	exec   *recExec
	stored provider.SensitivityTier
	// snap, when set, replaces the real WorktreeManager.Snapshot.
	snap func(context.Context, jobs.FenceFunc, jobs.ResourceLease, int64, []string) (jobs.SnapshotResult, error)
	d    *Dispatcher
	ref  JobRef
}

func redirectHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
}

func openFileDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rigJob(id string) jobs.Job {
	return jobs.Job{
		ID: id, State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1, Capabilities: []string{"code"},
		MutableScope: "repo:/tmp/x", RiskClass: "normal", MinTaskClass: "code", NodeRequirements: "{}",
		TimeoutSeconds: 60, CostCeiling: 1, Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}
}

// newRigRepo creates the git repository (symlink-resolved, with a committer
// identity) holding one seed commit and returns its root.
func newRigRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "rig@example.invalid")
	runGit(t, repo, "config", "user.name", "rig")
	writeRepoFile(t, repo, "README.md", "seed\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// openRigDBs opens and migrates the CI and jobs databases and seeds the job.
func (r *streamRig) openRigDBs() {
	r.t.Helper()
	ctx, clock := context.Background(), newTestClock()
	r.ciDB, r.jobsDB = openFileDB(r.t, "ci.db"), openFileDB(r.t, "jobs.db")
	r.store = jobs.NewStore(r.jobsDB)
	for _, err := range []error{
		ApplyMigrationSchema(ctx, r.ciDB, migrate.SQLiteEmitter{}, clock, "", ""),
		jobs.ApplyJobsSchema(ctx, r.jobsDB, migrate.SQLiteEmitter{}, clock, "", ""),
		jobs.ApplyOutboxSchema(ctx, r.jobsDB, migrate.SQLiteEmitter{}, clock, "", ""),
		r.store.PutJob(ctx, rigJob("job-1")),
	} {
		if err != nil {
			r.t.Fatalf("seeding the rig databases: %v", err)
		}
	}
}

// newRig builds the repo, the jobs stack, a leased worktree and a Dispatcher
// over a recording executor.
func newRig(t *testing.T) *streamRig {
	t.Helper()
	redirectHome(t)
	ctx, clock := context.Background(), newTestClock()
	repo := newRigRepo(t)
	r := &streamRig{t: t, repo: repo, base: gitOutput(t, repo, "rev-parse", "HEAD"), exec: &recExec{},
		attn: &fakePusher{}, stored: provider.SensitivityInternal}
	r.openRigDBs()
	r.lease = jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-1", Epoch: 1, State: jobs.LeaseHeld}
	if err := r.store.PutLease(ctx, r.lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	r.lm = jobs.NewLeaseManager(r.store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	var err error
	if r.wm, err = jobs.NewWorktreeManager(r.store, nil, nil, nil, r.lm.Fence); err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}
	w, err := r.wm.Create(ctx, r.lease, repo)
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	r.wt = w.Path
	r.bus = events.New(storetest.NewMemStore(), clock)
	r.build(r.exec)
	r.ref = JobRef{
		JobID: "job-1", ProjectID: "proj", RepoRoot: repo, WorktreeRoot: r.wt, Lease: r.lease, LeaseEpoch: 1,
		AttemptID: "att-1", AttemptGeneration: 1, BaseCommit: r.base,
		ScopePrefixes: []string{"README.md", "docs/", "src/", "pkg/", "internal/"},
		PlannedRisk:   jobs.RiskClassLow, Sensitivity: provider.SensitivityInternal,
	}
	return r
}

// build (re)creates the Dispatcher over exec.
func (r *streamRig) build(exec SubJobExecutor) {
	r.t.Helper()
	snapshot := r.wm.Snapshot
	if r.snap != nil {
		snapshot = r.snap
	}
	d, err := NewDispatcher(DispatcherDeps{
		CIDB: r.ciDB, JobsDB: r.jobsDB, Snapshot: snapshot, Fence: r.lm.Fence,
		StoredSensitivity: func(context.Context, string) (provider.SensitivityTier, error) { return r.stored, nil },
		Executor:          exec, Bus: r.bus, Attention: r.attn, Clock: newTestClock(),
	})
	if err != nil {
		r.t.Fatalf("NewDispatcher: %v", err)
	}
	r.d = d
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// commit writes files into the leased worktree, commits them and sets the
// ref's checkpoint commit to the new commit.
func (r *streamRig) commit(files map[string]string) string {
	r.t.Helper()
	for rel, content := range files {
		writeRepoFile(r.t, r.wt, rel, content)
	}
	runGit(r.t, r.wt, "add", ".")
	runGit(r.t, r.wt, "commit", "-q", "-m", "work")
	sha := gitOutput(r.t, r.wt, "rev-parse", "HEAD")
	r.ref.CheckpointCommit = sha
	return sha
}

func (r *streamRig) count(db *sql.DB, query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		r.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// streamEvents returns the decoded payloads of every dispatcher event of
// kind published so far.
func (r *streamRig) streamEvents(kind events.EventKind) []streamEventPayload {
	r.t.Helper()
	all, err := r.bus.Replay(context.Background(), EventNamespace, 0)
	if err != nil {
		r.t.Fatalf("Replay: %v", err)
	}
	var out []streamEventPayload
	for _, ev := range all {
		if ev.Kind != kind {
			continue
		}
		var p streamEventPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			r.t.Fatalf("decode %s: %v", kind, err)
		}
		out = append(out, p)
	}
	return out
}

func (r *streamRig) checkpoint() (CandidateSnapshot, error) {
	return r.d.Checkpoint(context.Background(), r.ref, RequirementModel{})
}

func TestNewDispatcherNamesTheMissingField(t *testing.T) {
	r := newRig(t)
	deps := DispatcherDeps{CIDB: r.ciDB, JobsDB: r.jobsDB, Snapshot: r.wm.Snapshot, Fence: r.lm.Fence,
		StoredSensitivity: func(context.Context, string) (provider.SensitivityTier, error) { return 0, nil },
		Executor:          r.exec, Bus: r.bus, Attention: r.attn, Clock: newTestClock()}
	if _, err := NewDispatcher(deps); err != nil {
		t.Fatalf("complete deps: %v", err)
	}
	cases := map[string]func(*DispatcherDeps){
		"CIDB": func(d *DispatcherDeps) { d.CIDB = nil }, "JobsDB": func(d *DispatcherDeps) { d.JobsDB = nil },
		"Snapshot": func(d *DispatcherDeps) { d.Snapshot = nil }, "Fence": func(d *DispatcherDeps) { d.Fence = nil },
		"StoredSensitivity": func(d *DispatcherDeps) { d.StoredSensitivity = nil },
		"Executor":          func(d *DispatcherDeps) { d.Executor = nil }, "Bus": func(d *DispatcherDeps) { d.Bus = nil },
		"Attention": func(d *DispatcherDeps) { d.Attention = nil }, "Clock": func(d *DispatcherDeps) { d.Clock = nil },
	}
	for field, mutate := range cases {
		bad := deps
		mutate(&bad)
		_, err := NewDispatcher(bad)
		if err == nil || !strings.Contains(err.Error(), "non-nil "+field) {
			t.Fatalf("missing %s: err = %v, want it to name the field", field, err)
		}
	}
}
