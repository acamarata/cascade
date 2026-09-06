package daemon

// Purpose: covers RegisterContextScopeHandler and gitRootExec
//   (context_scope.go), the E/S-08.T4 daemon-side registration this
//   package's own coverage floor requires real tests for -- not just a
//   no-panic constructor check. Not named in this ticket's files_scope
//   (which lists only daemon.go under "change"), added by necessity to
//   avoid regressing internal/daemon's measured coverage floor with
//   untested new lines, the same class of authorized-write-set extension
//   daemon_rpc_test.go's own header documents for T-3 (R-14.113/R-14.133);
//   flagged in this ticket's journal for T0 to ratify.
// Constraints: Art.2 -- the happy-path test drives a real git repository
//   (mirroring internal/context/discover_test.go's runGit pattern) and a
//   real modernc SQLite file under t.TempDir(), through the REAL
//   registry.Dispatch entry point, never by calling scope.ContextScopeShow
//   directly. Art.7.1 -- no real network listener.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runGit runs a git subcommand in dir, failing the test on error. Mirrors
// internal/context/discover_test.go's helper of the same name (this
// package cannot import that one, being in a different package).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestRegisterContextScopeHandler_RegistersAndDispatches proves
// RegisterContextScopeHandler's registration line matters: it drives the
// method through the REAL registry.Dispatch entry point (not a direct
// scope.ContextScopeShow call), against a real git repository and a real
// modernc SQLite cascade.db file under a fresh DataDir.
func TestRegisterContextScopeHandler_RegistersAndDispatches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repoDir := filepath.Join(t.TempDir(), "repo")
	runGit(t, repoDir, "init", "-q", ".")

	paths := fakePathsFor(t, "")
	clock := runtime.NewSystemClock()
	registry := rpc.NewRegistry()

	db, err := RegisterContextScopeHandler(registry, paths, clock)
	if err != nil {
		t.Fatalf("RegisterContextScopeHandler: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if !registry.Registered(ContextScopeMethod) {
		t.Fatal("RegisterContextScopeHandler did not register ContextScopeMethod")
	}

	if _, err := os.Stat(filepath.Join(paths.DataDir(), "cascade.db")); err != nil {
		t.Errorf("cascade.db was not created under DataDir: %v", err)
	}

	params := scope.ShowParams{Cwd: repoDir, Session: "s1"}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextScopeMethod, Params: raw, ID: json.RawMessage(`1`),
	})
	if errObj != nil {
		t.Fatalf("Dispatch(%s) returned error: %+v", ContextScopeMethod, errObj)
	}
	got, ok := result.(scope.SessionScope)
	if !ok {
		t.Fatalf("Dispatch result type = %T, want scope.SessionScope", result)
	}
	// The repo's git root was never registered as a scope.RepositoryRecord,
	// so the honest, real result is the R-16.3 `general` restriction --
	// proving the handler ran the actual resolution path rather than a
	// stub, without fabricating a bespoke graph fixture (matching
	// internal/integration.TestContextScopeRealCounterparts's own choice).
	if got.Kind != scope.ScopeKindGeneral {
		t.Errorf("Kind = %q, want %q (unregistered git root)", got.Kind, scope.ScopeKindGeneral)
	}
	if got.Session != "s1" {
		t.Errorf("Session = %q, want %q (caller-supplied field preserved)", got.Session, "s1")
	}
}

// TestRegisterContextScopeHandler_WiringProof is this file's own mutation
// lever: dispatching ContextScopeMethod against a registry
// RegisterContextScopeHandler never touched must fail with "method not
// found", proving the test above exercises the actual registration line
// rather than a vacuously-passing fixture.
func TestRegisterContextScopeHandler_WiringProof(t *testing.T) {
	registry := rpc.NewRegistry()
	if registry.Registered(ContextScopeMethod) {
		t.Fatal("ContextScopeMethod already registered on a fresh registry")
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextScopeMethod, ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch succeeded against an unregistered method, want method-not-found")
	}
}

// TestRegisterContextScopeHandler_DataDirFailure exercises the
// os.MkdirAll error path: DataDir points under a plain file, so MkdirAll
// can never create it. Asserts a typed cascade.KindUnavailable error and a
// nil *sql.DB (no leaked handle on this failure branch).
func TestRegisterContextScopeHandler_DataDirFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	paths := fakePaths{root: blocker, socketPath: ""}
	registry := rpc.NewRegistry()

	db, err := RegisterContextScopeHandler(registry, paths, runtime.NewSystemClock())
	if err == nil {
		t.Fatal("RegisterContextScopeHandler with an unwritable DataDir = nil error, want one")
	}
	if db != nil {
		t.Error("RegisterContextScopeHandler returned a non-nil db alongside an error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v, ok=%v, want KindUnavailable", kind, ok)
	}
	if registry.Registered(ContextScopeMethod) {
		t.Error("ContextScopeMethod was registered despite a setup failure")
	}
}

// TestGitRootExec_RealRepository drives gitRootExec (the production
// scope.GitRootFunc this file wires for the daemon side) against a real
// git repository, resolving to the same root `git rev-parse
// --show-toplevel` itself would report.
func TestGitRootExec_RealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repoDir := filepath.Join(t.TempDir(), "repo")
	runGit(t, repoDir, "init", "-q", ".")
	resolvedRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("resolve repo dir: %v", err)
	}

	got := gitRootExec(context.Background(), repoDir)
	resolvedGot, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve gitRootExec result %q: %v", got, err)
	}
	if resolvedGot != resolvedRepoDir {
		t.Errorf("gitRootExec(%q) = %q, want %q", repoDir, resolvedGot, resolvedRepoDir)
	}
}

// TestGitRootExec_FallsBackOnFailure exercises the non-repository branch:
// `git rev-parse` fails or is absent, so gitRootExec falls back to cwd
// itself rather than returning an error (never an error, per its own doc
// comment).
func TestGitRootExec_FallsBackOnFailure(t *testing.T) {
	got := gitRootExec(context.Background(), "/nonexistent-cascade-test-path")
	if got != "/nonexistent-cascade-test-path" {
		t.Errorf("gitRootExec on a nonexistent path = %q, want the cwd fallback unchanged", got)
	}
}
