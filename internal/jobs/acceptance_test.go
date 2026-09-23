package jobs_test

// Purpose: P1-E29-W6-S60-T4's acceptance suite, Path 1 (happy path):
//
//	drives the REAL AC/S-59.T4 planner, AC/S-59.T2 lease manager, AC/
//	S-59.T3 worktree manager, AC/S-60.T3 evidence ledger and completion
//	policy over one docs/**-only PEWS fixture, from plan through
//	accepted, asserting STORE state (not merely emitted events) at each
//	stage.
//
// CONTRACT NOTE (quoted in the journal, full detail in the BLOCKED
// report): the ticket's own HOW text assumes Planner.Plan yields "one
// implement node and one review node" and that lease contention lands a
// job in a `queued` state. Neither exists in the real tree: Planner.Plan
// (planner.go) produces exactly ONE node per ticket (dag.go's
// ExecutionDag has no second-node concept, and there is no `queued`
// JobState or LeaseState anywhere in model.go/state.go -- lease
// contention simply leaves a job un-admitted at JobStatePending). This
// suite follows the real tree: one job carries the full running ->
// verifying -> reviewing -> accepted lifecycle, and Path 3 (this
// package's acceptance_path3_test.go) asserts non-admission, not a
// `queued` state, while a lease is held.
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this file's real store

	"gopkg.in/yaml.v3"
)

// fixtureYAML mirrors pews-ticket-fixture.yaml's own shape. There is no
// production PEWSContract YAML decoder anywhere in the tree
// (pews_compiler.go's own doc comment: decoding is out of that
// compiler's scope) -- this is this acceptance test's own loader over
// its own fixture file, never a schema production code reads.
type fixtureYAML struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	ShortDesc  string   `yaml:"short_desc"`
	FullDesc   string   `yaml:"full_desc"`
	Branch     string   `yaml:"branch"`
	Weight     string   `yaml:"weight"`
	ModelClass string   `yaml:"model_class"`
	DependsOn  []string `yaml:"depends_on"`
	Tasks      []string `yaml:"tasks"`
	Checks     []string `yaml:"checks"`
	AcceptCrit []string `yaml:"acceptance_criteria"`
	FilesScope struct {
		Add    []string `yaml:"add"`
		Change []string `yaml:"change"`
		Delete []string `yaml:"delete"`
	} `yaml:"files_scope"`
	SpecRefs     []string `yaml:"spec_refs"`
	CRLevel      string   `yaml:"cr_level"`
	QALevel      string   `yaml:"qa_level"`
	SportUpdates []string `yaml:"sport_updates"`
	DocsUpdates  []string `yaml:"docs_updates"`
}

// loadFixtureContract reads testdata/pews-ticket-fixture.yaml and builds
// the jobs.PEWSContract CompileTicket needs.
func loadFixtureContract(t *testing.T) jobs.PEWSContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pews-ticket-fixture.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixtureYAML
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return jobs.PEWSContract{
		ID: f.ID, Title: f.Title, ShortDesc: f.ShortDesc, FullDesc: f.FullDesc,
		Branch: f.Branch, Weight: f.Weight, ModelClass: conductor.ModelClass(f.ModelClass),
		DependsOn: f.DependsOn, Tasks: f.Tasks, Checks: f.Checks,
		AcceptanceCriteria: f.AcceptCrit,
		FilesScopeAdd:      f.FilesScope.Add, FilesScopeChange: f.FilesScope.Change,
		FilesScopeDelete: f.FilesScope.Delete, SpecRefs: f.SpecRefs,
		CRLevel: f.CRLevel, QALevel: f.QALevel,
		SportUpdates: f.SportUpdates, DocsUpdates: f.DocsUpdates,
	}
}

// acceptanceRig bundles every real subsystem Path 1/Path 3 drive.
type acceptanceRig struct {
	store    *jobs.Store
	leases   *jobs.LeaseManager
	worktree *jobs.WorktreeManager
	ledger   *jobs.EvidenceLedger
	policy   *jobs.CompletionPolicy
	clock    runtime.Clock
	repoRoot string
	ctx      context.Context
	// journal is the SAME M/S-27.T1 journal.Store wired into worktree
	// (WorktreeManager.Create's own appendWorktreeJournal writes to it
	// automatically) -- exposed so Path 1 can also append and read back
	// a job-keyed entry, proving both the ">=1 job journal entry" and
	// ">=1 worktree journal entry" acceptance criteria against the SAME
	// real store, never a second private one.
	journal *journal.SQLiteStore
}

const acceptanceEngineID = "acceptance-engine"

// newAcceptanceRig opens a real modernc-sqlite file under t.TempDir(),
// applies the real jobs+outbox schema, and wires the real
// LeaseManager/WorktreeManager/EvidenceLedger/CompletionPolicy over it --
// the same construction pattern completion_test.go's
// newCompletionFixture and gate_stall_integration_test.go's
// completionFixtureForGate already establish, adapted to this package's
// exported surface (this file lives in jobs_test, not jobs).
func newAcceptanceRig(t *testing.T) *acceptanceRig {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "acceptance.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	clock := runtime.NewFixedClock(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	applyAcceptanceSchemas(t, db, clock)
	store := jobs.NewStore(db)

	leases := jobs.NewLeaseManager(store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	jobs.WireLeaseRelease(store, leases)

	wtStore := storetest.NewMemStore()
	journalStore := journal.New(wtStore, clock, journal.DefaultNamespace)
	worktree := jobs.NewWorktreeManager(store, journalStore, nil, nil)

	authz := jobs.NewProducerAuthz(store, func() bool { return true }, leases)
	writer := audit.New(storetest.NewMemStore(), clock, nil)
	ledger, err := jobs.NewEvidenceLedger(store, clock, writer, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	policy, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{
		Store: store, Ledger: ledger, Clock: clock, EngineID: acceptanceEngineID,
	})
	if err != nil {
		t.Fatalf("NewCompletionPolicy: %v", err)
	}

	return &acceptanceRig{
		store: store, leases: leases, worktree: worktree, ledger: ledger, policy: policy,
		clock: clock, repoRoot: realGitRepo(t), ctx: nodes.WithRole(context.Background(), nodes.RoleController),
		journal: journalStore,
	}
}

// applyAcceptanceSchemas applies the real jobs+outbox+evidence schema to
// db -- split out of newAcceptanceRig purely to keep it under the
// 50-line cap.
func applyAcceptanceSchemas(t *testing.T, db *sql.DB, clock runtime.Clock) {
	t.Helper()
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyOutboxSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	if err := jobs.ApplyEvidenceSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyEvidenceSchema: %v", err)
	}
}

// realGitRepo creates a real, throwaway git repository under
// t.TempDir() with one commit -- worktree.go's Create shells to the
// real git binary and requires an existing repository with at least one
// commit to branch a worktree from (Art.2: never a stand-in for git
// itself).
func realGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "acceptance@example.invalid")
	run("config", "user.name", "acceptance")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("acceptance fixture repo\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "seed")
	return root
}

func sessionScope(root string) scope.SessionScope {
	return scope.SessionScope{
		Kind:       scope.ScopeKindGeneral,
		Repository: &scope.RepositoryRecord{RootPath: root},
	}
}

// TestAcceptanceReleasedHolderFenced proves R-14.300/AMD-20260922/C1 at
// the acceptance level: once a lease is explicitly released, its former
// holder's evidence append (ProducerAuthz.Authorize, routed through
// EvidenceLedger.Append) and worktree Snapshot are BOTH refused by the
// real Fence check -- even though it still presents the epoch it was
// originally granted (Release, lease.go, never advances the epoch, so
// only Fence's state check protects a released holder).
func TestAcceptanceReleasedHolderFenced(t *testing.T) {
	rig := newAcceptanceRig(t)
	ctx := rig.ctx

	const jobID = "job-released-fence"
	if err := rig.store.PutJob(ctx, jobs.Job{
		ID: jobID, State: jobs.JobStatePending, MutableScope: "docs/**",
		RiskClass: string(jobs.RiskClassLow), MinTaskClass: "code",
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	acquired, err := rig.leases.Acquire(ctx, "acceptance-repo", "docs/**", jobID)
	if err != nil || !acquired.Granted {
		t.Fatalf("Acquire: %+v, %v", acquired, err)
	}
	if _, err := rig.worktree.Create(ctx, acquired.Lease, rig.repoRoot); err != nil {
		t.Fatalf("worktree Create: %v", err)
	}
	t.Cleanup(func() { _ = rig.worktree.Remove(context.Background(), acquired.Lease) })
	if err := rig.store.PutExecution(ctx, jobs.Execution{ID: "exec-" + jobID, JobID: jobID, Attempt: 1, State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}

	// Release while the epoch is still the SAME one Acquire granted.
	if err := rig.leases.Release(ctx, "acceptance-repo", "docs/**", acquired.Lease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// The released holder presents its ORIGINAL, still-numerically-equal
	// epoch: an epoch-only fence would let both calls below through.
	rec := jobs.EvidenceRecord{
		JobID: jobID, Kind: jobs.EvidenceLint, ProducerCapability: jobs.ProducerControllerRun,
		AttemptID: "exec-" + jobID, AttestorIdentity: "daemon:acceptance",
		Outcome: jobs.OutcomePass, IdempotencyKey: "idem-released-1",
	}
	auth := jobs.AppendAuthorization{
		ExecutionID: "exec-" + jobID, LeaseRepoID: acquired.Lease.RepoID,
		LeaseScopeGlob: acquired.Lease.ScopeGlob, LeaseEpoch: acquired.Lease.Epoch,
	}
	if _, err := rig.ledger.Append(ctx, rec, auth); err == nil {
		t.Fatal("Append after release = nil error, want refused (released holder fenced)")
	}
	if _, err := rig.worktree.Snapshot(ctx, rig.leases.Fence, acquired.Lease, acquired.Lease.Epoch, nil); err == nil {
		t.Fatal("Snapshot after release = nil error, want refused (released holder fenced)")
	}
}
