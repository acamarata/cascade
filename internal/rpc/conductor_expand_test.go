package rpc

// Purpose: conductor.expand's registration against a real evidence.Fetcher:
// dispatch through the Registry matches a direct Expand call byte-for-byte,
// and a second registration attempt is refused (this file's own
// duplicate-registration-fails-startup case, per conductor_expand.go's
// doc comment).
//
// SPORT: rpc/conductor-expand (ADD), R-21.68.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/evidence"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"

	_ "modernc.org/sqlite"
)

// initTestGitRepo creates a real one-commit git repository under
// t.TempDir() and returns its dir and commit sha -- split out of
// newTestFetcher to stay under the funlen cap.
func initTestGitRepo(t *testing.T) (repoDir, commit string) {
	t.Helper()
	repoDir = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cascade-test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=cascade-test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "initial")
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return repoDir, string(out[:40])
}

func newTestFetcher(t *testing.T) *evidence.Fetcher {
	t.Helper()
	repoDir, commit := initTestGitRepo(t)

	dbPath := filepath.Join(t.TempDir(), "expand-rpc.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	if err := evidence.ApplySchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store := evidence.NewStore(db, clock)
	if err := store.PutClaim(ctx, evidence.Claim{
		ID: "CLM-RPC", Statement: "s", Type: evidence.ClaimObservedFact, Confidence: 1,
		ProducedBy: evidence.ProducedBy{RunID: "run-1"}, DataClass: evidence.DataClassInternal,
	}); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if err := store.PutEvidence(ctx, evidence.Evidence{
		ID: "EVD-RPC", ClaimID: "CLM-RPC",
		Source: evidence.Source{Type: evidence.SourceGit, Repository: repoDir, Commit: commit, Path: "f.txt",
			Locator: evidence.Locator{Kind: evidence.LocatorLines, Start: 1, End: 1}},
		ContentHash: evidence.ContentHash([]byte("hello")), DataClass: evidence.DataClassInternal,
	}); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	return evidence.NewFetcher(store, nil)
}

func TestConductorExpand_MatchesDirectCall(t *testing.T) {
	fetcher := newTestFetcher(t)
	reg := NewRegistry()
	if err := RegisterConductorExpand(reg, fetcher); err != nil {
		t.Fatalf("RegisterConductorExpand: %v", err)
	}
	if !reg.Registered(MethodConductorExpand) {
		t.Fatal("conductor.expand not registered")
	}

	direct, err := fetcher.Expand(context.Background(), "CLM-RPC", "")
	if err != nil {
		t.Fatalf("direct Expand: %v", err)
	}
	directJSON, err := json.Marshal(direct)
	if err != nil {
		t.Fatalf("marshal direct: %v", err)
	}

	req, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"conductor.expand","params":{"claim_id":"CLM-RPC","page_token":""},"id":1}`))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	result, dispatchErr := reg.Dispatch(context.Background(), req)
	if dispatchErr != nil {
		t.Fatalf("Dispatch: %+v", dispatchErr)
	}
	dispatchedJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal dispatched: %v", err)
	}

	if string(dispatchedJSON) != string(directJSON) {
		t.Errorf("dispatch result != direct Expand call:\n dispatch=%s\n direct  =%s", dispatchedJSON, directJSON)
	}
}

func TestConductorExpand_MissingClaimID(t *testing.T) {
	fetcher := newTestFetcher(t)
	reg := NewRegistry()
	if err := RegisterConductorExpand(reg, fetcher); err != nil {
		t.Fatalf("RegisterConductorExpand: %v", err)
	}
	req, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"conductor.expand","params":{},"id":1}`))
	_, dispatchErr := reg.Dispatch(context.Background(), req)
	if dispatchErr == nil {
		t.Fatal("Dispatch with no claim_id = nil error, want invalid-input")
	}
}

func TestConductorExpand_DuplicateRegistrationFailsStartup(t *testing.T) {
	fetcher := newTestFetcher(t)
	reg := NewRegistry()
	if err := RegisterConductorExpand(reg, fetcher); err != nil {
		t.Fatalf("first RegisterConductorExpand: %v", err)
	}
	err := RegisterConductorExpand(reg, fetcher)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second RegisterConductorExpand = %v, want typed conflict", err)
	}
}
