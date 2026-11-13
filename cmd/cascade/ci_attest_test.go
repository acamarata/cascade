package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestFixture is a git repo, a HOME whose data dir holds an enrolled node
// and the jobs schema, and the node's custody (file vault under t.TempDir).
type attestFixture struct {
	repo, home, dataDir, commit, tree string
	node                              nodes.Identity
	keys                              *nodes.NodeKeystore
	start                             time.Time
}

// skipIfGitExecDenied is a no-op; git failures are always fatal so that
// tests pass or fail explicitly rather than skipping under harness sandboxes.
func skipIfGitExecDenied(t *testing.T, _ error) {
	t.Helper()
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := c.CombinedOutput()
	skipIfGitExecDenied(t, err)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newAttestFixture(t *testing.T) *attestFixture {
	t.Helper()
	f := &attestFixture{repo: t.TempDir(), home: t.TempDir(), start: testkit.NewRealClock().Now().UTC().Truncate(time.Second)}
	f.dataDir = filepath.Join(f.home, ".cascade", "data")
	gitT(t, f.repo, "init", "-q")
	_ = os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("one\n"), 0o600) // git add below fails loudly if this did
	gitT(t, f.repo, "add", "a.txt")
	gitT(t, f.repo, "commit", "-q", "-m", "one")
	f.commit, f.tree = gitT(t, f.repo, "rev-parse", "HEAD"), gitT(t, f.repo, "rev-parse", "HEAD^{tree}")
	keys, err := nodes.NewNodeKeystore(secrets.Config{Service: "cascade-attest-cli-test", Dir: t.TempDir(), ForceFileVault: true})
	if err != nil || keys.BackendName() != "file-vault" {
		t.Fatalf("custody = %v, %v; want the temp-dir file-vault", keys, err)
	}
	id, priv, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil || keys.Store(context.Background(), id.NodeID, priv) != nil {
		t.Fatalf("identity: %v", err)
	}
	f.node, f.keys = id, keys
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(f.dataDir), testkit.NewFrozenClock(f.start))
	if _, err := records.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	writeTestDesignation(t, f.dataDir) // a designated controller home
	return f
}

func (f *attestFixture) openDB(t *testing.T) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(f.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dataDir, "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, testkit.NewFrozenClock(f.start), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	return db
}

// accept issues and consumes a nonce for repoID on the fixture commit.
func (f *attestFixture) accept(t *testing.T, db *sql.DB, repoID, checkpoint string) string {
	t.Helper()
	nonces, _ := jobs.NewAttestationNonceStore(db, testkit.NewFrozenClock(f.start))
	ctx := context.Background()
	nonce, _, err := nonces.Issue(ctx, "job-"+repoID, checkpoint, repoID, "spec-1", "policy-v1", []string{"lint", "unit"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.BeginTx(ctx, nil)
	if err := nonces.ConsumeOnAcceptTx(ctx, tx, nonce, "job-"+repoID, f.commit, f.tree, f.start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nonce
}

func nonceDump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out string
	err := db.QueryRowContext(context.Background(), `SELECT group_concat(nonce || '|' || ifnull(consumed_at, '-') || '|' || ifnull(accepted_commit, '-'), ';')
		FROM attestation_nonce`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// runBinary runs the real cascade binary in the fixture repo with a clean
// environment (HOME redirected to the fixture).
func (f *attestFixture) runBinary(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	c := exec.Command(bin, append([]string{"ci", "attest", "verify"}, args...)...)
	c.Dir = f.repo
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.home, "USERPROFILE=" + f.home, "GIT_CONFIG_NOSYSTEM=1"}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), out.String()
	}
	if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return 0, out.String()
}

// buildAttestBinary builds the real cascade binary into this test's temp dir.
func buildAttestBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skips the real-binary build")
	}
	bin := filepath.Join(t.TempDir(), "cascade"+exeSuffix())
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func exeSuffix() string { return map[bool]string{true: ".exe"}[goruntime.GOOS == "windows"] }

func TestCiAttestVerify(t *testing.T) {
	bin := buildAttestBinary(t)
	f := newAttestFixture(t)
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	if code, out := f.runBinary(t, bin, f.commit, "--repo-id", "repo-a"); code != 0 {
		t.Fatalf("zero records without --require: exit %d, want 0\n%s", code, out)
	}
	if code, out := f.runBinary(t, bin, f.commit, "--repo-id", "repo-a", "--require"); code != 1 {
		t.Fatalf("zero records with --require: exit %d, want 1\n%s", code, out)
	}
	nonce := f.accept(t, db, "repo-a", "ckpt-1")
	row := f.store(t, db, "repo-a", nonce, "ckpt-1", 0)
	before := nonceDump(t, db)
	if code, out := f.runBinary(t, bin, f.commit, "--repo-id", "repo-a", "--require"); code != 0 || !strings.Contains(out, "eligible=1") {
		t.Fatalf("verified record: exit %d, want 0 with one eligible\n%s", code, out)
	}
	if after := nonceDump(t, db); after != before {
		t.Fatalf("ci attest verify changed attestation_nonce:\n%s\n%s", before, after)
	}
	tampered := strings.Replace(row.CanonicalJSON, `"target_selection":"all"`, `"target_selection":"none"`, 1)
	if _, err := db.Exec(`UPDATE ci_attestation SET canonical_json = ?, id = ?, target_selection = 'none' WHERE id = ?`, tampered,
		attest.RowID(attest.SignedAttestation{CanonicalJSON: []byte(tampered)}), row.ID); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{f.commit, "--repo-id", "repo-a", "--require"}, {f.commit, "--repo-id", "repo-a"}} {
		if code, out := f.runBinary(t, bin, args...); code != 1 {
			t.Fatalf("tampered stored row %v: exit %d, want 1\n%s", args, code, out)
		}
	}
}

// runInProcess runs the verb through the real command tree over fixture f.
func (f *attestFixture) runInProcess(t *testing.T, args ...string) error {
	t.Helper()
	paths, err := cruntime.NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return filepath.Join(f.home, ".cascade")
		}
		return ""
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := ciAttestVerifyDeps{Paths: paths, Clock: cruntime.NewSystemClock(), Git: func(ctx context.Context, a ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", f.repo}, a...)...).Output()
		return strings.TrimSpace(string(out)), err
	}}
	cmd := ci.NewCICmd(ci.CmdDeps{AttestVerify: ciAttestVerify(deps)})
	addCIAttestVerifyFlags(cmd)
	cmd.SetArgs(append([]string{"attest", "verify"}, args...))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd.Execute()
}

func TestCiAttestVerifyBindsRepo(t *testing.T) {
	f := newAttestFixture(t)
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	nonceA := f.accept(t, db, "repo-a", "ckpt-a")
	f.accept(t, db, "repo-b", "ckpt-b")
	f.store(t, db, "repo-a", nonceA, "ckpt-a", 0)
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-b"); cascade.ExitCode(err) != 1 {
		t.Fatalf("repo A record verified for --repo-id repo-b: %v (exit %d), want exit 1", err, cascade.ExitCode(err))
	}
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a"); err != nil {
		t.Fatalf("repo A record for --repo-id repo-a: %v, want exit 0", err)
	}
	if err := f.runInProcess(t, f.commit, "--require"); !errors.Is(err, jobs.ErrRepoNotRegistered) {
		t.Fatalf("no repo id anywhere: %v, want ErrRepoNotRegistered", err)
	}
	g := newAttestFixture(t)
	gdb := g.openDB(t)
	t.Cleanup(func() { _ = gdb.Close() })
	g.store(t, gdb, "repo-b", g.accept(t, gdb, "repo-a", "ckpt-x"), "ckpt-x", 0)
	for _, repo := range []string{"repo-a", "repo-b"} {
		if err := g.runInProcess(t, g.commit, "--require", "--repo-id", repo); cascade.ExitCode(err) != 1 {
			t.Fatalf("record of repo-b on repo-a's accepted nonce verified for %s: %v", repo, err)
		}
	}
	gitT(t, f.repo, "config", "cascade.gate.repoId", "repo-a")
	if err := f.runInProcess(t, f.commit, "--require"); err != nil {
		t.Fatalf("repo id from git config: %v, want exit 0", err)
	}
	for _, args := range [][]string{{"--not-a-rev"}, {"--", "-x"}, {"--", "--output=/dev/null"}} {
		if err := f.runInProcess(t, args...); cascade.ExitCode(err) == 0 {
			t.Fatalf("option-shaped revision %v was accepted", args)
		}
	}
	if err := f.runInProcess(t, strings.Repeat("f", 40), "--repo-id", "repo-a"); cascade.ExitCode(err) != 2 {
		t.Fatalf("unknown commit: %v, want exit 2", err)
	}
}

func TestCiAttestVerifyEligibility(t *testing.T) {
	f := newAttestFixture(t)
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	nonce := f.accept(t, db, "repo-a", "ckpt-1")
	f.store(t, db, "repo-a", nonce, "ckpt-1", 1)
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a"); cascade.ExitCode(err) != 1 {
		t.Fatalf("failing re-run counted as eligible: %v", err)
	}
	if err := f.runInProcess(t, f.commit, "--repo-id", "repo-a"); cascade.ExitCode(err) != 1 {
		t.Fatalf("a failing but valid record without --require: %v, want exit 1 (stored, none eligible)", err)
	}
	unaccepted := f.store(t, db, "repo-a", "never-issued", "ckpt-2", 0)
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a"); cascade.ExitCode(err) != 1 {
		t.Fatalf("record with no accepted fact (%s) counted as eligible: %v", unaccepted.ID, err)
	}
	if _, err := db.Exec(`UPDATE ci_attestation SET exit_status = 0 WHERE nonce = ?`, nonce); err != nil {
		t.Fatal(err)
	}
	err := f.runInProcess(t, f.commit, "--repo-id", "repo-a")
	if cascade.ExitCode(err) != 1 || !strings.Contains(fmt.Sprint(err), "disagree") {
		t.Fatalf("column tampered to passing: %v, want exit 1 naming the disagreement", err)
	}
}
