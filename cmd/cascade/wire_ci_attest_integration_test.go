//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/spf13/cobra"
)

func attestedDaemon(t *testing.T, constructor func(secrets.Config) (*nodes.NodeKeystore, error)) (*daemonWiring, *ciDaemonRig) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r := &ciDaemonRig{deps: daemonDeps{Paths: fakeDaemonPaths{root: t.TempDir()},
		Getenv: func(string) string { return "" }, Environ: func() []string { return nil }, Clock: runtime.SystemClock{}},
		tk: &ciFakeTicker{c: make(chan struct{})}}
	saved := newCICheckpointTicker
	newCICheckpointTicker = func() runtime.Ticker { return r.tk }
	t.Cleanup(func() { newCICheckpointTicker = saved })
	r.repo, _ = filepath.EvalSymlinks(t.TempDir())
	skipIfGitExecDenied(t, exec.Command("git", "--version").Run())
	ciRigGit(t, r.repo, "init", "-q", "-b", "main")
	ciRigGit(t, r.repo, "commit", "--allow-empty", "-qm", "seed")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(r.repo))
	r.deps.SecretsDir = t.TempDir()
	r.deps.NewKeystore = constructor
	addTestRegistration(t, daemonRegistration{Name: "test-attestation-local-executor", Phase: phaseSupervised, Order: 21,
		Wire: func(w *daemonWiring) error { return installAttestationExecutor(t, w) }})
	seen := observeWiring(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, cleanups, err := composeDaemon(ctx, r.deps, nil)
	t.Cleanup(func() { cancel(); runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	return <-seen, r
}

func installAttestationExecutor(t *testing.T, w *daemonWiring) error {
	t.Helper()
	attention, err := newCIAttentionPusher(w.Store, w.Clock, w.Bus, nil)
	if err != nil {
		return err
	}
	rt, err := daemon.NewCISubsystem(w.Ctx, w.Deps.Fleet, w.Deps.Config, w.Bus, attention, w.Clock)
	if err != nil {
		return err
	}
	executor, err := newLocalTrueExecutor(t, w, rt)
	if err != nil {
		return err
	}
	if err := rt.SetExecutor(executor); err != nil {
		return err
	}
	w.Deps.CI = rt
	return nil
}

// newLocalTrueExecutor is the controller's local executor with every
// requirement command replaced by `true`, so acceptance leaves real run rows.
func newLocalTrueExecutor(t *testing.T, w *daemonWiring, rt *daemon.CIRuntime) (ci.SubJobExecutor, error) {
	t.Helper()
	commands := map[ci.RequirementKind][]string{}
	for _, kind := range []ci.RequirementKind{ci.RequirementFormat, ci.RequirementLint, ci.RequirementCompile, ci.RequirementUnit, ci.RequirementIntegration, ci.RequirementArchitecture, ci.RequirementSecurity} {
		commands[kind] = []string{"true"}
	}
	return ci.NewLocalSubJobExecutor(ci.LocalExecutorDeps{CIDB: rt.CIDB(), Clock: w.Clock,
		Exec: ci.ShellExecutor{}, Commands: commands, Environ: os.Environ(), RunRoot: t.TempDir(), ModCache: t.TempDir(),
		Populate: func(context.Context, string, []string) error { return nil }})
}

func attestationCheckpoint(t *testing.T, w *daemonWiring, r *ciDaemonRig) ci.JobRef {
	t.Helper()
	if _, err := scope.NewGraphStore(w.Deps.Fleet.JobsDB()).EnsureRepository(context.Background(), r.repo, "", contextgit.GitCommonDir); err != nil {
		t.Fatal(err)
	}
	ref := hookRecordedCheckpoint(t, w, r)
	hookEvidence(t, w, ref.JobID)
	return ref
}

func TestAcceptanceAttestationEndToEnd(t *testing.T) {
	w, r := attestedDaemon(t, nodes.NewNodeKeystore)
	ref := attestationCheckpoint(t, w, r)
	response := hookDispatch(t, w, ref.JobID)
	if response.Deny || hookStored(t, w, ref.JobID).State != jobs.JobStateAccepted {
		t.Fatalf("acceptance failed: %+v", response)
	}
	rows, _ := jobs.NewCiAttestationStore(w.Deps.Fleet.JobsDB())
	stored, err := rows.ByCommitSHA(context.Background(), ref.CheckpointCommit)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored attestations: %v %v", stored, err)
	}
	row := stored[0]
	if row.AttestorClass != "enrolled-node" || row.CheckpointID == "" || row.Nonce == "" {
		t.Fatalf("incomplete attestation: %+v", row)
	}
	var consumed int
	if err := w.Deps.Fleet.JobsDB().QueryRow(`SELECT count(*) FROM attestation_nonce WHERE nonce=? AND consumed_at IS NOT NULL`, row.Nonce).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("nonce not consumed: %d %v", consumed, err)
	}
	deps := ciAttestVerifyDeps{Paths: w.Paths, Clock: w.Clock, Git: func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = r.repo
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}}
	var output bytes.Buffer
	cmd := &cobra.Command{Use: "verify"}
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("require", true, "")
	cmd.Flags().String("repo-id", row.RepoID, "")
	if err := ciAttestVerify(deps)(cmd, []string{row.CommitSHA}); err != nil {
		t.Fatalf("ci attest verify --require: %v", err)
	}
	if !strings.Contains(output.String(), "eligible=1") {
		t.Fatalf("verification output: %s", output.String())
	}
}

func guardedAcceptanceKeystore(t *testing.T, configs *[]secrets.Config) func(secrets.Config) (*nodes.NodeKeystore, error) {
	t.Helper()
	return func(config secrets.Config) (*nodes.NodeKeystore, error) {
		*configs = append(*configs, config)
		if !config.ForceFileVault {
			return nil, cascade.New(cascade.KindPermissionDenied, "test: non-file custody refused before Store")
		}
		keys, err := nodes.NewNodeKeystore(config)
		if err == nil && keys.BackendName() != "file-vault" {
			return nil, cascade.New(cascade.KindIntegrity, "test: wrong custody backend")
		}
		return keys, err
	}
}

func attestedHighPolicy(t *testing.T) (*daemonWiring, *ciAcceptanceVerifier, *jobs.CompletionPolicy, jobs.TransitionRequest, jobs.CiAttestationRow) {
	t.Helper()
	w, r := attestedDaemon(t, nodes.NewNodeKeystore)
	ref := attestationCheckpoint(t, w, r)
	job := hookStored(t, w, ref.JobID)
	job.RiskClass = "high"
	if err := w.Deps.Fleet.Jobs().PutJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	v, err := newCIAcceptanceVerifier(w)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := v.VerifyAcceptance(context.Background(), job.ID)
	if err != nil || !verdict.Passed {
		t.Fatalf("attestation verdict: %+v %v", verdict, err)
	}
	ledger, _, err := completionLedger(context.Background(), w.Store, w.Clock, w.Paths, w.Deps.Fleet.Leases(), completionController(w.Paths))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{Store: w.Deps.Fleet.Jobs(), Ledger: ledger, Clock: w.Clock,
		EngineID: "spec-test", Acceptance: testAcceptanceVerifier{verdict: verdict}, RequireAcceptance: true, AttestationTrust: v})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := jobs.NewCiAttestationStore(w.Deps.Fleet.JobsDB())
	stored, err := rows.ByCommitSHA(context.Background(), ref.CheckpointCommit)
	if err != nil || len(stored) != 1 {
		t.Fatalf("attestation rows: %v %v", stored, err)
	}
	req := jobs.TransitionRequest{Job: &job, Target: jobs.JobStateAccepted, Caller: jobs.PolicyEngineIdentity{EngineID: "spec-test"},
		PlannedRiskClass: jobs.RiskClassHigh, ActualFootprint: jobs.ChangeFootprint{ChangedPaths: []string{"pkg/feature.go"}},
		LeaseScopePrefixes: []string{"pkg/"}, CheckpointID: verdict.CheckpointID, TreeHash: verdict.TreeHash}
	return w, v, cp, req, stored[0]
}

func TestAcceptanceNonceBindsResolvedSpec(t *testing.T) {
	for _, change := range []string{"state-and-attempt", "risk_class", "mutable_scope"} {
		t.Run(change, func(t *testing.T) {
			w, _, cp, req, row := attestedHighPolicy(t)
			factStore, _ := jobs.NewAttestationNonceStore(w.Deps.Fleet.JobsDB(), w.Clock)
			fact, ok, err := factStore.Issued(context.Background(), row.Nonce)
			if err != nil || !ok || fact.RepoID != row.RepoID || fact.JobSpecHash != jobs.JobSpecHash(*req.Job, nil) || len(fact.PlanKinds) == 0 {
				t.Fatalf("issued fact: %+v %v", fact, err)
			}
			job := *req.Job
			job.ConsecutiveFailedAttempts++
			job.UpdatedAt++
			if change == "risk_class" {
				job.RiskClass = "critical"
			}
			if change == "mutable_scope" {
				job.MutableScope = "pkg/**"
			}
			if err := w.Deps.Fleet.Jobs().PutJob(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			err = cp.Transition(context.Background(), req)
			if change == "state-and-attempt" {
				if err != nil {
					t.Fatalf("mutable row metadata broke stable spec: %v", err)
				}
			} else if err == nil {
				t.Fatal("mutated job spec accepted")
			}
		})
	}
}

func TestCrossRegistrationReplayRefused(t *testing.T) {
	w, _, cp, req, row := attestedHighPolicy(t)
	other := t.TempDir()
	ciRigGit(t, other, "init", "-q", "-b", "main")
	ciRigGit(t, other, "commit", "--allow-empty", "-qm", "other registration")
	repository, err := scope.NewGraphStore(w.Deps.Fleet.JobsDB()).EnsureRepository(context.Background(), other, "", contextgit.GitCommonDir)
	if err != nil || repository.ID == "" || repository.ID == row.RepoID {
		t.Fatalf("second registration: %+v %v", repository, err)
	}
	if _, err := w.Deps.Fleet.JobsDB().Exec(`UPDATE attestation_nonce SET repo_id=? WHERE nonce=?`, repository.ID, row.Nonce); err != nil {
		t.Fatal(err)
	}
	if err := cp.Transition(context.Background(), req); err == nil {
		t.Fatal("registration A attestation satisfied registration B")
	}
	if hookStored(t, w, req.Job.ID).State != jobs.JobStateReviewing {
		t.Fatal("replay changed state")
	}
}

func TestCachedAttestationRequiresEvidence(t *testing.T) {
	w, v, _, req, _ := attestedHighPolicy(t)
	if _, err := w.Deps.Fleet.JobsDB().Exec(`DELETE FROM jobs_evidence WHERE kind='ci_attestation'`); err != nil {
		t.Fatal(err)
	}
	if verdict, err := v.VerifyAcceptance(context.Background(), req.Job.ID); err == nil || verdict.Passed {
		t.Fatalf("orphan cached attestation accepted: %+v %v", verdict, err)
	}
}
