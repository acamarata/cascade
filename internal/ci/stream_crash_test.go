// Purpose: TestStreamCrashInjection, the SEC-33 re-exec recipe for the
// dispatcher: a child copy of this test binary runs Checkpoint and is
// SIGKILLed immediately before or after one of four sites (outbox intent,
// sub-job start, ci_job insert, outbox confirm); the parent reopens the
// SAME on-disk databases, runs Dispatcher.Resume and asserts exactly one
// ci_run per (attempt, kind). The kill sites are a test-only database/sql
// driver wrapper and test-only executors: nothing in production code knows
// about them.
//
// SPORT: internal.ci.Dispatcher.Resume/TESTED (P1-CI-01).
package ci

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	crashEnvSite = "CASCADE_CI_STREAM_CRASH_SITE" // "<site>:<before|after>"
	crashEnvDir  = "CASCADE_CI_STREAM_CRASH_DIR"
	crashDriver  = "cascade-ci-stream-kill"
)

// killPoint is the child's one injected kill: a site and a phase.
type killPoint struct{ site, phase string }

var (
	killOnce sync.Once
	kill     killPoint
)

func killSelf() {
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = p.Kill()
	}
	select {}
}

// killDriver wraps the real sqlite driver and kills the process around the
// statement of the configured database site.
type killDriver struct{ inner driver.Driver }

func (d killDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &killConn{Conn: c}, nil
}

type killConn struct {
	driver.Conn
	inTx, pending bool
}

func (c *killConn) hit(q string, args []driver.NamedValue) bool {
	switch kill.site {
	case "intent":
		return strings.Contains(q, "INSERT INTO jobs_outbox")
	case "job", "jobtx":
		return strings.Contains(q, "INSERT INTO ci_job")
	case "attempt":
		return strings.Contains(q, "INSERT INTO ci_stream_attempt")
	case "confirm":
		return strings.Contains(q, "UPDATE jobs_outbox SET state") && len(args) > 0 && args[0].Value == "confirmed"
	}
	return false
}

func (c *killConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	hit := c.hit(q, args)
	if hit && kill.phase == "before" {
		killSelf()
	}
	res, err := ex.ExecContext(ctx, q, args)
	if hit && kill.phase == "after" && err == nil {
		if c.inTx && kill.site != "jobtx" {
			// "jobtx" kills inside the open transaction, before its commit.
			c.pending = true
		} else {
			killSelf()
		}
	}
	return res, err
}

func (c *killConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	qr, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return qr.QueryContext(ctx, q, args)
}

func (c *killConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

func (c *killConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return &killTx{Tx: tx, c: c}, nil
}

type killTx struct {
	driver.Tx
	c *killConn
}

func (t *killTx) Commit() error {
	err := t.Tx.Commit()
	t.c.inTx = false
	if err == nil && t.c.pending {
		killSelf()
	}
	return err
}

func (t *killTx) Rollback() error {
	t.c.inTx, t.c.pending = false, false
	return t.Tx.Rollback()
}

// crashFixture is the on-disk state the child and the parent share.
type crashFixture struct{ dir, repo, commit, base string }

func (f crashFixture) ref() JobRef {
	return JobRef{
		JobID: "crash-job", RepoRoot: f.repo, Lease: jobs.ResourceLease{RepoID: f.repo, ScopeGlob: "**", Holder: "crash-job", Epoch: 1},
		LeaseEpoch: 1, AttemptID: "att", AttemptGeneration: 1, CheckpointCommit: f.commit, BaseCommit: f.base,
		ScopePrefixes: []string{"docs/"}, PlannedRisk: jobs.RiskClassLow, Sensitivity: provider.SensitivityInternal,
	}
}

// dispatcher builds a Dispatcher over the fixture's databases opened through
// driverName, with a counting shell stand-in and an optional first-step hook.
func (f crashFixture) dispatcher(t *testing.T, driverName string, onStep func()) (*Dispatcher, *lockedExec, *sql.DB, *sql.DB) {
	t.Helper()
	open := func(name string) *sql.DB {
		db, err := sql.Open(driverName, filepath.Join(f.dir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	ciDB, jobsDB := open("ci.db"), open("jobs.db")
	clock := newTestClock()
	fx := &lockedExec{fn: func(ExecRequest) ExecResult {
		if onStep != nil {
			onStep()
		}
		return ExecResult{}
	}}
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: ciDB, Clock: clock, Exec: fx, Commands: allKindCommands(), Environ: []string{"PATH=" + os.Getenv("PATH")},
		RunRoot: filepath.Join(f.dir, "runs"), ModCache: filepath.Join(f.dir, "mod"),
		Populate: func(context.Context, string, []string) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewLocalSubJobExecutor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(f.dir, "runs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sub := SubJobExecutor(ex)
	if kill.site == "subjob" && kill.phase == "before" {
		sub = killBeforeExec{ex}
	}
	d, err := NewDispatcher(DispatcherDeps{
		CIDB: ciDB, JobsDB: jobsDB, Executor: sub, Bus: events.New(storetest.NewMemStore(), clock), Attention: &fakePusher{}, Clock: clock,
		Snapshot: func(context.Context, jobs.FenceFunc, jobs.ResourceLease, int64, []string) (jobs.SnapshotResult, error) {
			return jobs.SnapshotResult{TreeHash: "unused"}, nil
		},
		Fence: func(context.Context, string, string, int64) error { return nil },
		StoredSensitivity: func(context.Context, string) (provider.SensitivityTier, error) {
			return provider.SensitivityInternal, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d, fx, ciDB, jobsDB
}

// TestStreamCrashChild is the child half; an ordinary run is a no-op.
func TestStreamCrashChild(t *testing.T) {
	spec := os.Getenv(crashEnvSite)
	if spec == "" {
		return
	}
	site, phase, _ := strings.Cut(spec, ":")
	kill = killPoint{site: site, phase: phase}
	killOnce.Do(func() {
		inner, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open inner: %v", err)
		}
		sql.Register(crashDriver, killDriver{inner: inner.Driver()})
		_ = inner.Close()
	})
	f := crashFixture{dir: os.Getenv(crashEnvDir)}
	f.repo = gitOutput(t, filepath.Join(f.dir, "repo"), "rev-parse", "--show-toplevel")
	f.commit = gitOutput(t, f.repo, "rev-parse", "HEAD")
	f.base = gitOutput(t, f.repo, "rev-parse", "HEAD~1")
	var onStep func()
	if site == "subjob" && phase == "after" {
		onStep = killSelf
	}
	d, _, _, _ := f.dispatcher(t, crashDriver, onStep)
	_, err := d.Checkpoint(context.Background(), f.ref(), RequirementModel{})
	t.Fatalf("child was not killed at %s (Checkpoint returned %v)", spec, err)
}

func newCrashFixture(t *testing.T) crashFixture {
	t.Helper()
	redirectHome(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "crash@example.invalid")
	runGit(t, repo, "config", "user.name", "crash")
	writeRepoFile(t, repo, "README.md", "seed\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	writeRepoFile(t, repo, "docs/a.md", "a\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "work")
	f := crashFixture{dir: dir, repo: gitOutput(t, repo, "rev-parse", "--show-toplevel")}
	f.commit, f.base = gitOutput(t, repo, "rev-parse", "HEAD"), gitOutput(t, repo, "rev-parse", "HEAD~1")
	ciDB, jobsDB := openFileDB2(t, filepath.Join(dir, "ci.db")), openFileDB2(t, filepath.Join(dir, "jobs.db"))
	ctx, clock := context.Background(), newTestClock()
	for _, step := range []error{
		ApplyMigrationSchema(ctx, ciDB, migrate.SQLiteEmitter{}, clock, "", ""),
		jobs.ApplyJobsSchema(ctx, jobsDB, migrate.SQLiteEmitter{}, clock, "", ""),
		jobs.ApplyOutboxSchema(ctx, jobsDB, migrate.SQLiteEmitter{}, clock, "", ""),
		jobs.NewStore(jobsDB).PutJob(ctx, rigJob("crash-job")),
	} {
		if step != nil {
			t.Fatalf("seeding the crash databases: %v", step)
		}
	}
	_ = ciDB.Close()
	_ = jobsDB.Close()
	return f
}

func openFileDB2(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func runCrashChild(t *testing.T, f crashFixture, spec string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStreamCrashChild$")
	cmd.Env = append(os.Environ(), crashEnvSite+"="+spec, crashEnvDir+"="+f.dir)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Success() {
		t.Fatalf("child for %s was not killed: err %v\n%s", spec, err, out)
	}
	if strings.Contains(string(out), "child was not killed") {
		t.Fatalf("child for %s ran past its kill site:\n%s", spec, out)
	}
}
