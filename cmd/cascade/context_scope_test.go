package main

// Purpose: `cascade context scope show`'s own tests: the R-14.166
//   reachability proof (mounted on the real root command tree) and the
//   Article-4 behavioral coverage floor test the ticket's checks name
//   (TestContextScopeCLIBehavioralCoverage70), driving the real cobra
//   command end to end against a temp-dir embedded cascade.db -- no real
//   socket, no "net"/"net/http" import (Art.7.2's default unit lane).
// SPORT: cmd.cascade.cmd.context-scope-show (ADD, per T-4 sport_updates).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// fakeContextScopePaths is a minimal runtime.PathProvider whose DataDir
// points at a temp dir, so the embedded path never touches a real
// cascade.db.
type fakeContextScopePaths struct{ dir string }

func (p fakeContextScopePaths) Root() string                         { return p.dir }
func (p fakeContextScopePaths) ConfigPath() string                   { return p.dir + "/config.toml" }
func (p fakeContextScopePaths) SocketPath() string                   { return p.dir + "/daemon.sock" }
func (p fakeContextScopePaths) DataDir() string                      { return p.dir }
func (p fakeContextScopePaths) LogDir() string                       { return p.dir + "/logs" }
func (p fakeContextScopePaths) StorageRoot(_ runtime.Profile) string { return p.dir }

// execRootContextScope drives the real root tree with the production
// `context` command swapped for one built from a temp-dir deps value, the
// same swap-on-the-real-root technique execRootDoctor uses so persistent
// global flags and guardUnknownSubcommands stay in the picture.
func execRootContextScope(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	// Force root.go's own daemonless probe (PersistentPreRunE, which reads
	// the REAL environment via runtime.NewDefaultPathProvider -- it does
	// not see this test's injected contextScopeDeps.Paths) at a socket
	// path this test controls, so the embedded-vs-daemon decision is
	// deterministic regardless of whether a real cascade daemon happens
	// to be running on the machine executing this test.
	t.Setenv("CASCADE_HOME", dir)
	t.Setenv("CASCADE_SOCKET", dir+"/daemon.sock")
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	existing, _, err := root.Find([]string{"context"})
	if err != nil {
		t.Fatalf("find context command: %v", err)
	}
	root.RemoveCommand(existing)
	deps := contextScopeDeps{
		Paths:   fakeContextScopePaths{dir: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		// DialContext intentionally left nil: t.Setenv above forces
		// root.go's real daemonless probe to find no daemon at this
		// test's own throwaway socket path, so every scenario below takes
		// the embedded path and never calls DialContext. Declaring a
		// fake dialer here would need a "net" import, which this
		// untagged _test.go file's default no-network unit lane (Art.7.2)
		// forbids.
	}
	cmd := newContextCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	execErr := root.Execute()
	return buf.String(), execErr
}

// TestContextScopeShowIsMountedOnRoot is the R-14.166 reachability proof:
// removing mountContextCmd's call from root.go's mountSubcommands makes
// `root.Find` fail to resolve "context scope show" at all. See this
// ticket's journal for the red-then-green result of actually performing
// that removal.
func TestContextScopeShowIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"context", "scope", "show"})
	if err != nil || found.Name() != "show" {
		t.Fatalf("context scope show is not mounted on the root command: found=%v err=%v", safeName(found), err)
	}
}

func safeName(c interface{ Name() string }) string {
	if c == nil {
		return "<nil>"
	}
	return c.Name()
}

// TestContextScopeCLIBehavioralCoverage70 drives `context scope show`
// through its human and --json output forms against an unresolved cwd
// (general scope, the always-reachable case in an isolated temp dir), and
// through its unknown-subcommand error path -- the Article-4 CLI
// behavioral floor.
func TestContextScopeCLIBehavioralCoverage70(t *testing.T) {
	dir := t.TempDir()

	t.Run("human", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "context", "scope", "show")
		if err != nil {
			t.Fatalf("cascade context scope show: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, "kind") || !strings.Contains(got, "general") {
			t.Errorf("human output missing kind=general:\n%s", got)
		}
	})

	t.Run("json", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "--json", "context", "scope", "show")
		if err != nil {
			t.Fatalf("cascade --json context scope show: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, `"kind": "general"`) {
			t.Errorf("--json output missing kind=general:\n%s", got)
		}
	})

	t.Run("with flags", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "--json", "context", "scope", "show", "--branch", "main", "--session", "s1")
		if err != nil {
			t.Fatalf("cascade context scope show --branch --session: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, `"session": "s1"`) {
			t.Errorf("--json output missing session=s1:\n%s", got)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		_, err := execRootContextScope(t, dir, "context", "scope", "bogus")
		if err == nil {
			t.Fatal("cascade context scope bogus = nil error, want invalid-input")
		}
	})

	t.Run("rejects positional args", func(t *testing.T) {
		_, err := execRootContextScope(t, dir, "context", "scope", "show", "extra-arg")
		if err == nil {
			t.Fatal("cascade context scope show extra-arg = nil error, want invalid-input")
		}
	})
}
