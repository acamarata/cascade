//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/client"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/spf13/cobra"
)

func newNonceVerifier(t *testing.T) (*ciAcceptanceVerifier, ci.JobRef, ci.CandidateSnapshot) {
	t.Helper()
	h := newCIRepoHarness(t)
	root, commit, _ := ciRepoGitFixture(t)
	var reply ciRepoRegisterReply
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
		t.Fatal(err)
	}
	db, clock := h.db(t), runtime.NewSystemClock()
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	seedTestJob(t, jobs.NewStore(db), "nonce-job", jobs.JobStateReviewing)
	registry, err := jobs.NewRepoRegistry(db, scope.NewGraphStore(db), contextgit.GitCommonDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	return &ciAcceptanceVerifier{jobsDB: db, registry: registry, clock: clock, issued: map[string]acceptanceNonce{}},
		ci.JobRef{JobID: "nonce-job", RepoRoot: root, CheckpointCommit: commit},
		ci.CandidateSnapshot{CheckpointID: "nonce-checkpoint", TreeHash: "nonce-tree"}
}

func TestCiRepoRegisterStoresBaseline(t *testing.T) {
	printed := ciRepoRegisterReply{RepoID: "id", Baseline: []ciRepoTip{{"refs/heads/main", "m1"}}, Excluded: []ciRepoExcludedTip{{"refs/heads/feature", "f1", "not-checked-out"}}}.String()
	if !strings.Contains(printed, "baseline refs/heads/main m1") || !strings.Contains(printed, "excluded refs/heads/feature f1 not-checked-out") {
		t.Fatal(printed)
	}
	t.Run("register", testCiRepoRegisterStoresBaseline)
}

func testCiRepoRegisterStoresBaseline(t *testing.T) {
	h := newCIRepoHarness(t)
	root, main, feature := ciRepoGitFixture(t)
	var reply ciRepoRegisterReply
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Baseline) != 1 || reply.Baseline[0] != (ciRepoTip{"refs/heads/main", main}) {
		t.Fatalf("baseline: %+v", reply)
	}
	out := reply.String()
	if !strings.Contains(out, "baseline refs/heads/main "+main) || !strings.Contains(out, "excluded refs/heads/feature "+feature+" not-checked-out") {
		t.Fatal(out)
	}
}

func TestCiRepoRegisterBaselineFlag(t *testing.T) {
	cmd := ci.NewCICmd(ci.CmdDeps{})
	register, _, err := cmd.Find([]string{"repo", "register"})
	if err != nil {
		t.Fatal(err)
	}
	if register.Flags().Lookup("baseline").Value.Type() != "stringSlice" {
		t.Fatal("baseline is not repeatable")
	}
	t.Run("register", testCiRepoRegisterBaselineFlag)
}

func testCiRepoRegisterBaselineFlag(t *testing.T) {
	h := newCIRepoHarness(t)
	root, main, feature := ciRepoGitFixture(t)
	var first, again ciRepoRegisterReply
	p := ciRepoRegisterParams{Path: root, Baseline: []string{"main", "feature"}}
	if err := h.call(context.Background(), "ci.repo_register", p, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Baseline) != 2 || len(first.Excluded) != 0 {
		t.Fatalf("explicit: %+v", first)
	}
	if first.Baseline[0] != (ciRepoTip{"refs/heads/feature", feature}) || first.Baseline[1] != (ciRepoTip{"refs/heads/main", main}) {
		t.Fatal(first.Baseline)
	}
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &again); err != nil {
		t.Fatal(err)
	}
	if again.RepoID != first.RepoID || !again.RegisteredAt.Equal(first.RegisteredAt) || len(again.Baseline) != 2 {
		t.Fatalf("re-register: %+v", again)
	}
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root, Baseline: []string{"main"}}, &again); err == nil {
		t.Fatal("baseline replacement succeeded")
	}
}

func TestCiRepoRegisterDefaultBaseline(t *testing.T) {
	notice := "baseline empty: HEAD is detached; only --baseline adds branches"
	if !strings.Contains((ciRepoRegisterReply{RepoID: "id", Detached: true}).String(), notice) {
		t.Fatal("detached notice missing")
	}
	if strings.Contains((ciRepoRegisterReply{RepoID: "id", Detached: true, Baseline: []ciRepoTip{{"refs/heads/main", "m1"}}}).String(), notice) {
		t.Fatal("detached notice printed beside a baseline")
	}
	t.Run("register", testCiRepoRegisterDefaultBaseline)
}

func testCiRepoRegisterDefaultBaseline(t *testing.T) {
	h := newCIRepoHarness(t)
	root, main, _ := ciRepoGitFixture(t)
	gitT(t, root, "checkout", "-q", "--detach", "main")
	gitT(t, root, "commit", "--allow-empty", "-qm", "detached")
	var reply ciRepoRegisterReply
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Baseline) != 0 || !reply.Detached || !strings.Contains(reply.String(), "baseline empty: HEAD is detached; only --baseline adds branches") {
		t.Fatalf("detached: %+v", reply)
	}
	if !strings.Contains(reply.String(), "excluded refs/heads/main "+main+" not-checked-out") {
		t.Fatal(reply.String())
	}
	root, main, _ = ciRepoGitFixture(t)
	gitT(t, root, "remote", "add", "origin", t.TempDir())
	gitT(t, root, "config", "branch.main.remote", "origin")
	gitT(t, root, "config", "branch.main.merge", "refs/heads/main")
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Baseline) != 0 || !strings.Contains(reply.String(), "excluded refs/heads/main "+main+" no-owned-test") {
		t.Fatalf("upstream: %+v", reply)
	}
}

func TestCiRepoRegisterNoDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	deps := statusDeps{Paths: fakeDaemonPaths{root: home}, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }, DialContext: client.UnixDialer}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("path", home, "")
	cmd.Flags().StringSlice("baseline", nil, "")
	err := ciRepoRegister(deps, elevatedCallDeps{})(cmd, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("missing daemon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "cascade.db")); !os.IsNotExist(err) {
		t.Fatal("CLI created registry database")
	}
}

func TestCiAttestVerifyRegistryFallback(t *testing.T) {
	h := newCIRepoHarness(t)
	f := newAttestFixture(t)
	f.dataDir = h.paths.DataDir()
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(f.dataDir), runtime.NewSystemClock())
	if _, err := records.Enroll(f.node, nodes.TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	var reply ciRepoRegisterReply
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: f.repo}, &reply); err != nil {
		t.Fatal(err)
	}
	db := f.openDB(t)
	nonce := f.accept(t, db, reply.RepoID, "checkpoint-fallback")
	f.store(t, db, reply.RepoID, nonce, "checkpoint-fallback", 0)
	_ = db.Close()
	deps := ciAttestVerifyDeps{Paths: h.paths, Clock: runtime.NewSystemClock(), Git: func(ctx context.Context, args ...string) (string, error) { return gitOut(ctx, f.repo, args...) }}
	cmd := ci.NewCICmd(ci.CmdDeps{AttestVerify: ciAttestVerify(deps)})
	addCIAttestVerifyFlags(cmd)
	cmd.SetArgs([]string{"attest", "verify", f.commit, "--require"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	unregistered, _, _ := ciRepoGitFixture(t)
	deps.Git = func(ctx context.Context, args ...string) (string, error) { return gitOut(ctx, unregistered, args...) }
	if _, err := resolveAttestSubject(context.Background(), deps, "HEAD", ""); err == nil {
		t.Fatal("unregistered fallback succeeded")
	}
}
