package ci

// Tests for the resolving SubJobExecutor (P1-CI-03): node lease lifecycle,
// per-run Environment provenance and the explicit non-pass terminal.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

func trusted(context.Context, SubJob) (provider.Provenance, error) {
	return provider.ProvenanceTrusted, nil
}

// newTestResolving builds the resolving executor over a fresh ci_results
// database. A nil fleet or trust stays nil.
func newTestResolving(t *testing.T, r ExecutorResolver, runners RunnerSet, fleet *fakeFleet,
	trust func(context.Context, SubJob) (provider.Provenance, error)) SubJobExecutor {
	t.Helper()
	db := openFileDB(t, "ci.db")
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	d := ResolvingDeps{CIDB: db, Clock: newTestClock(), SourceTrust: trust,
		Fingerprint: func(context.Context) (string, error) { return testFP, nil }}
	var f leaseFleet
	if fleet != nil {
		f = fleet
	}
	return NewResolvingSubJobExecutor(r, runners, f, d)
}

func testSubJob(t *testing.T, kind RequirementKind) SubJob {
	return SubJob{Kind: kind,
		Ref: JobRef{JobID: "job-1", ProjectID: "proj-1", RepoRoot: t.TempDir(), CheckpointCommit: strings.Repeat("a", 40),
			Sensitivity: provider.SensitivityInternal, Lease: jobs.ResourceLease{RepoID: "repo-1"}},
		Snapshot: CandidateSnapshot{AttemptID: "att-1", TreeHash: strings.Repeat("b", 40)}}
}

// TestExecutorReleasesPairedNodeLease: a granted node lease is committed
// before the run and released after it, after a failed run, and after a
// failed commit (where the runner never starts).
func TestExecutorReleasesPairedNodeLease(t *testing.T) {
	cases := []struct {
		name      string
		runErr    error
		commitErr error
		want      []string
		runs      int
	}{
		{"terminal", nil, nil, []string{"lease", "commit:lease-1", "release:lease-1"}, 1},
		{"run fails", errors.New("node lost"), nil, []string{"lease", "commit:lease-1", "release:lease-1"}, 1},
		{"commit fails", nil, errors.New("lease expired"), []string{"lease", "commit:lease-1", "release:lease-1"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fleet := &fakeFleet{commitErr: c.commitErr}
			node := &recRunner{res: SubJobResult{RunID: -1, RepoID: 1, Passed: true}, err: c.runErr}
			runners := RunnerSet{ExecutorNode: node}
			rig := newResolverRig(t, runners, fleet, nil)
			_, err := newTestResolving(t, rig.r, runners, fleet, trusted).Run(context.Background(), testSubJob(t, RequirementUnit))
			if (c.runErr != nil || c.commitErr != nil) != (err != nil) {
				t.Fatalf("Run error = %v", err)
			}
			if !slices.Equal(fleet.events, c.want) || node.n() != c.runs {
				t.Fatalf("fleet events = %v, runs %d; want %v, %d", fleet.events, node.n(), c.want, c.runs)
			}
		})
	}
}

// TestExecutorEnvironmentProvenance: every result produced through the
// resolver carries the resolved executor's Environment, whatever the
// runner itself reported.
func TestExecutorEnvironmentProvenance(t *testing.T) {
	lied := SubJobResult{RunID: -3, RepoID: 9, Passed: true, ExecutorKind: "local", Env: Environment{RunnerKind: "forged", Ephemeral: true}}
	cases := []struct {
		name    string
		runners RunnerSet
		public  bool
		want    Environment
	}{
		{"local", RunnerSet{ExecutorLocal: &recRunner{res: lied}}, false,
			Environment{RunnerKind: ExecutorLocal, IsolationClass: IsolationCleanCheckoutSameUID, ToolchainFP: testFP}},
		{"node", RunnerSet{ExecutorNode: &recRunner{res: lied}}, false,
			Environment{RunnerKind: ExecutorNode, IsolationClass: isolationNodeWorktree, NodeID: "node-7", ToolchainFP: testFP}},
		{"hosted", RunnerSet{ExecutorGitHubActions: &recRunner{res: lied}}, true,
			Environment{RunnerKind: ExecutorGitHubActions, IsolationClass: isolationHosted, Ephemeral: true, ToolchainFP: testFP}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fleet := &fakeFleet{}
			rig := newResolverRig(t, c.runners, fleet, &fakeReader{exists: true})
			sj := testSubJob(t, RequirementUnit)
			if c.public {
				sj.Ref.Sensitivity = provider.SensitivityPublic
			}
			res, err := newTestResolving(t, rig.r, c.runners, fleet, trusted).Run(context.Background(), sj)
			if err != nil || res.Env != c.want || res.ExecutorKind != string(c.want.RunnerKind) || res.NodeID != c.want.NodeID {
				t.Fatalf("Run = %+v, %v; want Env %+v", res, err, c.want)
			}
			if res.RunID != lied.RunID || !res.Passed {
				t.Fatalf("Run = %+v; the runner's run key and verdict must pass through", res)
			}
		})
	}
}

// TestExecutorUnresolvedTerminal: a sub-job no executor may run, and one
// whose runner reports ErrNoCapableExecutor, end as one completed/skipped
// ci_run with Passed=false; a redispatch reuses the same run.
func TestExecutorUnresolvedTerminal(t *testing.T) {
	incapable := &recRunner{err: errors.Join(errors.New("no [ci.local] command"), ErrNoCapableExecutor)}
	for name, runners := range map[string]RunnerSet{
		"no executor":      {ExecutorGitHubActions: &recRunner{}},
		"runner incapable": {ExecutorLocal: incapable},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newResolverRig(t, runners, nil, nil)
			exec := newTestResolving(t, rig.r, runners, nil, trusted)
			sj := testSubJob(t, RequirementIntegration)
			first, err := exec.Run(context.Background(), sj)
			if err != nil || first.Passed || first.ExecutorKind != executorKindUnresolved || first.RunID >= 0 {
				t.Fatalf("Run = %+v, %v; want an unresolved non-pass on a reserved local run", first, err)
			}
			again, err := exec.Run(context.Background(), sj)
			if err != nil || again.RunID != first.RunID || again.Passed {
				t.Fatalf("redispatch = %+v, %v; want run %d again, not passed", again, err, first.RunID)
			}
			var status, conclusion, job string
			db := exec.(*resolvingExecutor).d.CIDB
			if err := db.QueryRow(`SELECT r.status, r.conclusion, j.name FROM `+tableRun+` r JOIN `+tableJob+
				` j ON j.run_id = r.run_id WHERE r.run_id = ?`, first.RunID).Scan(&status, &conclusion, &job); err != nil {
				t.Fatal(err)
			}
			if status != string(RunStatusCompleted) || conclusion != string(ConclusionSkipped) || !strings.HasPrefix(job, "integration: not run: ") {
				t.Fatalf("row = %s/%s %q; want completed/skipped with the reason", status, conclusion, job)
			}
		})
	}
}

// TestExecutorFingerprintErrorIsNonPass: a toolchain-bound sub-job whose
// fingerprint cannot be computed ends as the explicit unresolved non-pass,
// not an error that redispatches on every start, and nothing runs.
func TestExecutorFingerprintErrorIsNonPass(t *testing.T) {
	local := &recRunner{res: SubJobResult{RunID: -1, RepoID: 1, Passed: true}}
	runners := RunnerSet{ExecutorLocal: local}
	rig := newResolverRig(t, runners, nil, nil)
	exec := newTestResolving(t, rig.r, runners, nil, trusted).(*resolvingExecutor)
	exec.d.Fingerprint = func(context.Context) (string, error) { return "", errors.New("exec: go: not found") }
	res, err := exec.Run(context.Background(), testSubJob(t, RequirementUnit))
	if err != nil || res.Passed || res.ExecutorKind != executorKindUnresolved || local.n() != 0 {
		t.Fatalf("Run = %+v, %v, runs %d; want an unresolved non-pass and nothing run", res, err, local.n())
	}
	if res, err := exec.Run(context.Background(), testSubJob(t, RequirementLint)); err != nil || !res.Passed || local.n() != 1 {
		t.Fatalf("lint (no toolchain) = %+v, %v; want it to run locally (positive control)", res, err)
	}
}
