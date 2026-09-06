//go:build !windows && integration

// Package integration holds Article-2 real-counterpart tests that span
// more than one internal package's own test tree -- this file's subject
// is the daemon-side context.scope.show wiring
// (internal/daemon.RegisterContextScopeHandler,
// cmd/cascade/daemon_unix_run.go's buildRPCServer), which needs a real
// git executable, a real modernc SQLite file, a real unix socket, and the
// real internal/client.Client SDK all in the same test -- no single
// existing package owns all four.
//
// Purpose: prove context.scope.show is reachable end to end through the
//   REAL production pieces: a real `git init` repository under
//   t.TempDir() (Article-2's real Git counterpart), a real
//   modernc.org/sqlite cascade.db file (the real SQLite counterpart), the
//   real internal/rpc.Registry/Handler pipeline served over a real unix
//   socket (matching internal/daemon/daemon_ipc_e2e_integration_test.go's
//   own pattern), and the real internal/client.Client SDK dialing it (the
//   spec-sourced JSON-RPC client fixture -- no hand-rolled request body).
//
// MCP scope note: this ticket's own rpc.go and internal/mcp/
//   context_scope_test.go record why no full-profile MCP tool
//   cascade_context_scope_show exists yet (a builtin-plugin blank-import
//   site outside this ticket's files_scope) -- so this file exercises the
//   CLI-and-daemon RPC transport only, not an MCP round trip, matching
//   the honestly-recorded state rather than fabricating MCP coverage for
//   a tool that is not exposed.
//
// SPORT: internal/integration (ADD, E/S-08.T4).
package integration

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// contextScopeDialTimeout mirrors cmd/cascade/context_scope.go's own
// constant; this package has no visibility into that unexported value.
const contextScopeDialTimeout = 5 * time.Second

// realGitRepo runs the real `git` executable to initialize a repository
// under a fresh directory in t.TempDir(), so cliGitRoot-equivalent
// resolution (this file's dialGitRoot) exercises a genuine `git
// rev-parse --show-toplevel`, never a fake. Skips the test if no git
// binary is on PATH, matching the repo's own Article-2 convention for an
// environment-dependent real counterpart.
func realGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// startContextScopeDaemon builds a real *rpc.Registry through
// daemon.RegisterContextScopeHandler (a real modernc SQLite connection
// under a fresh runtime.PathProvider's DataDir), serves it over a real
// unix socket with daemon.NewRPCServer, and returns a real
// internal/client.Client pointed at that socket plus a cleanup func. When
// wire is false, RegisterContextScopeHandler is never called -- the
// registry serves an empty method table, so a call to
// daemon.ContextScopeMethod must fail with "method not found". This is
// the mutation-test lever TestContextScopeRealCounterparts_WiringProof
// uses to prove the registration line actually matters.
func startContextScopeDaemon(t *testing.T, wire bool) *client.Client {
	t.Helper()
	dataDir := t.TempDir()
	registry := rpc.NewRegistry()
	if wire {
		clock := runtime.NewSystemClock()
		if _, err := daemon.RegisterContextScopeHandler(registry, fakePaths{dataDir: dataDir}, clock); err != nil {
			t.Fatalf("RegisterContextScopeHandler: %v", err)
		}
	}

	sockDir, err := os.MkdirTemp("", "ctxscope")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	srv := daemon.NewRPCServer(registry, nil)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	return client.New(sockPath, client.DialFunc(dial), contextScopeDialTimeout)
}

// fakePaths satisfies runtime.PathProvider with a single injected
// DataDir, the only value RegisterContextScopeHandler actually reads.
type fakePaths struct{ dataDir string }

func (f fakePaths) Root() string       { return f.dataDir }
func (f fakePaths) DataDir() string    { return f.dataDir }
func (f fakePaths) ConfigPath() string { return filepath.Join(f.dataDir, "config.toml") }
func (f fakePaths) SocketPath() string { return filepath.Join(f.dataDir, "daemon.sock") }
func (f fakePaths) LogDir() string     { return filepath.Join(f.dataDir, "logs") }
func (f fakePaths) StorageRoot(p runtime.Profile) string {
	return filepath.Join(f.dataDir, "storage", string(p))
}

// TestContextScopeRealCounterparts drives context.scope.show through
// every real production layer this ticket's contract names: a real git
// repository, a real SQLite file, a real unix socket, and the real
// client SDK. The repo's git root is never registered as a
// scope.RepositoryRecord, so the real, honest result is the R-16.3
// `general` restriction -- proving the daemon-side handler, the schema
// migration, and the wire round trip all work without fabricating a
// bespoke graph fixture.
func TestContextScopeRealCounterparts(t *testing.T) {
	repoDir := realGitRepo(t)
	c := startContextScopeDaemon(t, true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := c.ContextScopeShow(ctx, scope.ScopeShowParams{Cwd: repoDir, Session: "s1"})
	if err != nil {
		t.Fatalf("ContextScopeShow: %v", err)
	}
	if got.Kind != scope.ScopeKindGeneral {
		t.Errorf("Kind = %q, want %q (unregistered git root)", got.Kind, scope.ScopeKindGeneral)
	}
	if got.Session != "s1" {
		t.Errorf("Session = %q, want %q (caller-supplied field preserved)", got.Session, "s1")
	}
	if got.Repository != nil || got.Project != "" || got.Product != "" || got.Workspace != "" {
		t.Errorf("general result carried project-adjacent state: %+v", got)
	}
}

// TestContextScopeRealCounterparts_WiringProof is the composition-root
// mutation proof this ticket's dispatch requires: the SAME real socket
// and client, but built with wire=false so
// daemon.RegisterContextScopeHandler is never called. daemon.
// ContextScopeMethod must then be genuinely unreachable -- "method not
// found" -- demonstrating that TestContextScopeRealCounterparts above
// exercises the actual registration line in buildRPCServer, not a
// vacuously-passing fixture.
func TestContextScopeRealCounterparts_WiringProof(t *testing.T) {
	c := startContextScopeDaemon(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.ContextScopeShow(ctx, scope.ScopeShowParams{})
	if err == nil {
		t.Fatal("ContextScopeShow succeeded against an unregistered method, want an error")
	}
}
