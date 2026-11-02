//go:build !windows

package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

type ciRepoHarness struct {
	paths fakeDaemonPaths
	reg   *rpc.Registry
	call  func(context.Context, string, any, any) error
}

func newCIRepoHarness(t *testing.T) ciRepoHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	isolateInstallationKey(t)
	saved := daemonRegistrations
	daemonRegistrations = nil
	for _, registration := range saved {
		if registration.Name != "completion-gate" {
			daemonRegistrations = append(daemonRegistrations, registration)
		}
	}
	t.Cleanup(func() { daemonRegistrations = saved })
	paths := fakeDaemonPaths{root: t.TempDir()}
	reg, call := newElevatedTestRegistry(t, paths.DataDir())
	return ciRepoHarness{paths, reg, call}
}

func ciRepoGitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "commit", "--allow-empty", "-qm", "main")
	main := gitT(t, root, "rev-parse", "main")
	gitT(t, root, "checkout", "-qb", "feature")
	gitT(t, root, "commit", "--allow-empty", "-qm", "feature")
	feature := gitT(t, root, "rev-parse", "feature")
	gitT(t, root, "checkout", "-q", "main")
	if main == feature || main == "" {
		t.Fatal("fixture branches must differ")
	}
	return root, main, feature
}

func (h ciRepoHarness) db(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(h.paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestRepoRegisterIsElevated refuses unsigned and principal registration calls.
func TestRepoRegisterIsElevated(t *testing.T) {
	h := newCIRepoHarness(t)
	raw, _ := json.Marshal(ciRepoRegisterParams{Path: t.TempDir()})
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	_, eo := h.reg.Dispatch(ctx, &rpc.Request{Method: "ci.repo_register", Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unelevated register: %v", eo)
	}
	p := principal.Principal{ID: "test", Scope: "project:test"}
	_, eo = h.reg.Dispatch(principal.WithCaller(context.Background(), principal.Caller{Principal: &p}), &rpc.Request{Method: "ci.repo_register", Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("principal reached elevated handler: %v", eo)
	}
	if _, err := os.Stat(filepath.Join(h.paths.DataDir(), "cascade.db")); err == nil {
		var n int
		if err := h.db(t).QueryRow(`SELECT count(*) FROM jobs_repo_registration`).Scan(&n); err == nil && n != 0 {
			t.Fatalf("refused calls wrote %d rows", n)
		}
	}
	t.Run("elevated call", func(t *testing.T) {
		root, _, _ := ciRepoGitFixture(t)
		var reply ciRepoRegisterReply
		if err := h.call(ctx, "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.RepoID == "" {
			t.Fatal("missing registered identity")
		}
	})
}

// TestRepoRegistrySchemaOnFreshDB verifies identity schema precedes gate binding.
func TestRepoRegistrySchemaOnFreshDB(t *testing.T) {
	h := newCIRepoHarness(t)
	w := &daemonWiring{Paths: h.paths, Clock: runtime.NewSystemClock()}
	reg, closeDB, err := openCIRepoRegistry(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	for _, table := range []string{"context_repository", "jobs_repo_gate", "jobs_repo_registration", "jobs_repo_baseline"} {
		var n int
		if err := h.db(t).QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("fresh DB lacks %s: %d %v", table, n, err)
		}
	}
	if _, err := reg.ByID(context.Background(), "absent"); err != jobs.ErrRepoNotRegistered {
		t.Fatalf("fresh registry by id: %v", err)
	}
	t.Run("register and bind", func(t *testing.T) {
		root, _, _ := ciRepoGitFixture(t)
		if _, err := reg.ByPath(context.Background(), root); err != jobs.ErrRepoNotRegistered {
			t.Fatalf("fresh registry: %v", err)
		}
		var reply ciRepoRegisterReply
		if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
			t.Fatal(err)
		}
		if err := reg.BindGate(context.Background(), root, reply.RepoID); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCiRepoRegisterSignsWithInstallationKey independently verifies persisted signatures.
func TestCiRepoRegisterSignsWithInstallationKey(t *testing.T) {
	h := newCIRepoHarness(t)
	count := 0
	for _, m := range h.reg.Methods() {
		if m == "ci.repo_register" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ci.repo_register registered %d times", count)
	}
	t.Run("no daemon", TestCiRepoRegisterNoDaemon)
	t.Run("installation signature", func(t *testing.T) { testCIRepoInstallationSignature(t, h) })
}

func testCIRepoInstallationSignature(t *testing.T, h ciRepoHarness) {
	t.Helper()
	root, main, _ := ciRepoGitFixture(t)
	var reply ciRepoRegisterReply
	if err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply); err != nil {
		t.Fatal(err)
	}
	db := h.db(t)
	var storedRoot, at, ref, commit string
	var registrationSig, baselineSig []byte
	if err := db.QueryRow(`SELECT root,registered_at,sig FROM jobs_repo_registration WHERE repo_id=?`, reply.RepoID).Scan(&storedRoot, &at, &registrationSig); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref,commit_id,sig FROM jobs_repo_baseline WHERE repo_id=?`, reply.RepoID).Scan(&ref, &commit, &baselineSig); err != nil {
		t.Fatal(err)
	}
	if ref != "refs/heads/main" || commit != main || len(reply.Baseline) != 1 {
		t.Fatalf("baseline: %+v", reply)
	}
	pub, err := installationPublicKey(context.Background(), h.paths)
	if err != nil {
		t.Fatal(err)
	}
	registration, _ := json.Marshal(struct{ T, RepoID, Root, RegisteredAt string }{"registration", reply.RepoID, storedRoot, at})
	baseline, _ := json.Marshal(struct{ T, RepoID, RegisteredAt, Ref, Commit string }{"baseline", reply.RepoID, at, ref, commit})
	for _, v := range []struct{ message, sig []byte }{{registration, registrationSig}, {baseline, baselineSig}} {
		if !ed25519.Verify(pub, append([]byte("cascade.trust.record/v1\x1f"), v.message...), v.sig) {
			t.Fatal("installation signature does not verify")
		}
		if ed25519.Verify(pub, v.message, v.sig) {
			t.Fatal("signature lacks domain separation")
		}
	}
	if reply.RegisteredAt.IsZero() {
		t.Fatal("registration clock")
	}
	t.Run("ineligible controller", func(t *testing.T) { testCIRepoIneligible(t, h) })
}

func testCIRepoIneligible(t *testing.T, h ciRepoHarness) {
	t.Helper()
	root, _, _ := ciRepoGitFixture(t)
	path := filepath.Join(h.paths.DataDir(), "nodes", "controller_binding.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var reply ciRepoRegisterReply
	err := h.call(context.Background(), "ci.repo_register", ciRepoRegisterParams{Path: root}, &reply)
	if err == nil || !strings.Contains(err.Error(), "permission-denied") {
		t.Fatalf("ineligible registration: %v", err)
	}
	var count int
	if err := h.db(t).QueryRow(`SELECT count(*) FROM jobs_repo_registration`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("ineligible call wrote a row: %d", count)
	}
}
