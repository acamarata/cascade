package main

// Purpose: `cascade context sync [--check]`'s own tests (E/S-09.T4): the
//   R-14.166 reachability proof and the Article-4 behavioral coverage
//   floor, driving the real cobra command end to end against a temp-dir
//   embedded runtime — no real socket, no "net"/"net/http" import
//   (Art.7.2's default unit lane). Reuses execRootContextScope and
//   fakeContextScopePaths from context_scope_test.go (same package).
// SPORT: cli/context-sync/ADD (per T-4 sport_updates).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContextSyncIsMountedOnRoot is the R-14.166 reachability proof: this
// alone does not prove RPC wiring (the CLI falls back to the embedded
// runtime) — see this ticket's journal for the daemon-side mutation-proof
// result.
func TestContextSyncIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"context", "sync"})
	if err != nil || found.Name() != "sync" {
		t.Fatalf("context sync is not mounted on the root command: found=%v err=%v", safeName(found), err)
	}
}

// contextSyncFixtureDir seeds a two-tier tree under CASCADE_HOME so
// `cascade context sync` (which resolves cwd via os.Getwd, not
// deps.Paths) actually has something to report on.
func contextSyncFixtureDir(t *testing.T) (home, repo string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	repo = filepath.Join(home, "sites", "project", "repo")
	for _, dir := range []string{home, repo} {
		full := filepath.Join(dir, ".cascade")
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("seeding %s: %v", full, err)
		}
		if err := os.WriteFile(filepath.Join(full, "CASCADE.md"), []byte("## Style\n\nShort sentences.\n"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", full, err)
		}
	}
	return home, repo
}

// chdirTemp changes the process working directory to dir for the duration
// of the test and restores it afterward — needed because fetchContextSync
// resolves cwd via os.Getwd(), matching fetchContextSlice's own
// production convention rather than reading deps.Paths.
func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// pinContextSyncHome seeds a two-tier fixture, chdirs into its repo tier,
// and pins HOME to its own home dir (Art.7.1: the embedded path resolves
// the GCI tier through the real os.UserHomeDir — production convention,
// ComputeContextSync's own doc comment — so leaving the developer's actual
// $HOME in place would make the outcome depend on whatever real
// ~/.cascade/CASCADE.md happens to exist on the machine running it).
func pinContextSyncHome(t *testing.T) (repo string) {
	t.Helper()
	home, repo := contextSyncFixtureDir(t)
	chdirTemp(t, repo)
	t.Setenv("HOME", home)
	return repo
}

// TestContextSyncCLIBehavioralCoverage drives `context sync` and
// `context sync --check` through the --check non-zero-exit-on-stale
// contract and the regenerate-then-idempotent contract — the Article-4 CLI
// behavioral floor's core lifecycle.
func TestContextSyncCLIBehavioralCoverage(t *testing.T) {
	repo := pinContextSyncHome(t)

	t.Run("check reports stale on a never-synced tree", func(t *testing.T) {
		got, err := execRootContextScope(t, repo, "context", "sync", "--check")
		if err == nil {
			t.Fatalf("cascade context sync --check on a never-synced tree = nil error, want non-zero\noutput:\n%s", got)
		}
		if !strings.Contains(got, "stale") {
			t.Errorf("--check output missing a stale row:\n%s", got)
		}
	})

	t.Run("regenerate exits 0 and reports a delta", func(t *testing.T) {
		got, err := execRootContextScope(t, repo, "context", "sync")
		if err != nil {
			t.Fatalf("cascade context sync: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, "regenerated") {
			t.Errorf("regenerate output missing the delta report:\n%s", got)
		}
	})

	t.Run("second regenerate is idempotent", func(t *testing.T) {
		got, err := execRootContextScope(t, repo, "context", "sync")
		if err != nil {
			t.Fatalf("cascade context sync (second run): %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, "0 file(s) regenerated") {
			t.Errorf("second run output = %q, want 0 files regenerated", got)
		}
	})

	t.Run("check is clean after regenerate", func(t *testing.T) {
		got, err := execRootContextScope(t, repo, "context", "sync", "--check")
		if err != nil {
			t.Fatalf("cascade context sync --check after regenerate: %v\noutput:\n%s", err, got)
		}
		if strings.Contains(got, "\tstale\t") {
			t.Errorf("--check output still reports stale after regenerate:\n%s", got)
		}
	})
}

// TestContextSyncCLIErrorAndJSONForms covers the --json envelope shape and
// the positional-arg error path, split from the lifecycle test above to
// stay under Art.10.3's 50-line function cap.
func TestContextSyncCLIErrorAndJSONForms(t *testing.T) {
	repo := pinContextSyncHome(t)

	t.Run("json envelope", func(t *testing.T) {
		// Regenerate first so --check has something clean to report: its
		// own non-zero-exit-on-stale contract is covered by the lifecycle
		// test above, and is orthogonal to the envelope shape this
		// subtest checks.
		if _, err := execRootContextScope(t, repo, "context", "sync"); err != nil {
			t.Fatalf("cascade context sync (setup): %v", err)
		}
		got, err := execRootContextScope(t, repo, "--json", "context", "sync", "--check")
		if err != nil {
			t.Fatalf("cascade --json context sync --check: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, `"ok": true`) || !strings.Contains(got, `"drift"`) {
			t.Errorf("--json output missing the envelope/drift shape:\n%s", got)
		}
	})

	t.Run("rejects positional args", func(t *testing.T) {
		_, err := execRootContextScope(t, repo, "context", "sync", "extra-arg")
		if err == nil {
			t.Fatal("cascade context sync extra-arg = nil error, want invalid-input")
		}
	})
}
