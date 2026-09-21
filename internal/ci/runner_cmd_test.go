// Purpose: `ci run`/`ci status` cobra-command-tree tests, plus
// filterOnlySteps/resolveRepoRoot unit tests. The end-to-end tests build
// a standalone root *cobra.Command carrying the same global flags
// cmd/cascade/root.go registers (mirroring cmd/cascade/config's own
// config_test.go newTestRoot pattern), so NewCICmd's behavior is proven
// under the real global-flag contract without this package needing
// write access to cmd/cascade/root.go for the test itself.
// SPORT: internal.ci.NewCICmd/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestFilterOnlySteps_NoFlagsReturnsAll(t *testing.T) {
	steps := []RunnerStep{{Kind: StepLint}, {Kind: StepTest}, {Kind: StepBuild}}
	got := filterOnlySteps(steps, ciRunFlags{})
	if len(got) != 3 {
		t.Fatalf("filterOnlySteps with no flags = %v, want all 3 steps unchanged", got)
	}
}

func TestFilterOnlySteps_TestOnly(t *testing.T) {
	steps := []RunnerStep{{Kind: StepLint}, {Kind: StepTest}, {Kind: StepBuild}}
	got := filterOnlySteps(steps, ciRunFlags{TestOnly: true})
	if len(got) != 1 || got[0].Kind != StepTest {
		t.Fatalf("filterOnlySteps(--test-only) = %v, want exactly [test]", got)
	}
}

func TestResolveRepoRoot_ExplicitFlag(t *testing.T) {
	got, err := resolveRepoRoot("/explicit/path")
	if err != nil || got != "/explicit/path" {
		t.Fatalf("resolveRepoRoot(explicit) = (%q, %v), want (/explicit/path, nil)", got, err)
	}
}

func TestResolveRepoRoot_DefaultsToCwd(t *testing.T) {
	got, err := resolveRepoRoot("")
	if err != nil {
		t.Fatalf("resolveRepoRoot(\"\"): %v", err)
	}
	wantCwd, _ := os.Getwd()
	if got != wantCwd {
		t.Errorf("resolveRepoRoot(\"\") = %q, want the cwd %q", got, wantCwd)
	}
}

// newTestCIRoot builds a standalone root carrying the SAME persistent
// flags cmd/cascade/root.go registers, with NewCICmd mounted under it, a
// PathProvider rooted at homeDir, and a never-pay resolver that routes
// everything local (the private-repo case -- `ci run` proceeds).
func newTestCIRoot(t *testing.T, homeDir string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	return newTestCIRootWithRoutes(t, homeDir, localOnlyRoutes)
}

// newTestCIRootWithRoutes is newTestCIRoot with the policy resolver named
// explicitly, for the tests that assert a refusal.
func newTestCIRootWithRoutes(t *testing.T, homeDir string, routes RouteResolver) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{Use: "cascade"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().String("profile", "", "")
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().BoolP("quiet", "q", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")
	root.PersistentFlags().Bool("no-color", false, "")

	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return homeDir
		}
		return ""
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return homeDir, nil })
	if err != nil {
		t.Fatal(err)
	}
	deps := CmdDeps{
		Paths: paths, Getenv: getenv,
		// The ambient environment a step sees in these tests is PATH and
		// HOME only, filtered by the same allowlist production uses -- so
		// "true"/"false" resolve, and nothing else is reachable.
		Environ: testEnviron,
		Clock:   runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
		Routes:  routes,
	}
	root.AddCommand(NewCICmd(deps))

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetContext(context.Background())
	return root, &stdout, &stderr
}

// writeCILocalConfig writes a config.toml declaring explicit [ci.local]
// commands (portable "true"/"false" builtins -- no go.mod auto-detection
// needed), matching NewPathProvider's own ConfigPath() = Root()/
// config.toml resolution used by newTestCIRoot above.
func writeCILocalConfig(t *testing.T, homeDir string, testCmd string) {
	t.Helper()
	toml := "[ci.local]\nlint = [\"true\"]\ntest = [\"" + testCmd + "\"]\nbuild = [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(homeDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

// newTestCheckout makes a temp directory the local gate accepts: a real
// directory with a .git entry (ValidateRepoRoot's requirement).
func newTestCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCICmd_RunThenStatus_EndToEnd is the acceptance-criterion proof at
// the CLI-surface level: `ci run` executes real "true" sub-processes
// (ShellExecutor, no fake), writes source=local to a real cascade.db
// under a temp HOME, exits zero, and `ci status` then shows that same
// run with source "local".
func TestCICmd_RunThenStatus_EndToEnd(t *testing.T) {
	homeDir := t.TempDir()
	writeCILocalConfig(t, homeDir, "true")
	repoDir := newTestCheckout(t)

	root, stdout, _ := newTestCIRoot(t, homeDir)
	root.SetArgs([]string{"ci", "run", "--repo", repoDir})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("ci run: %v", err)
	}
	if !strings.Contains(stdout.String(), "PASSED") {
		t.Errorf("ci run output = %q, want it to contain PASSED", stdout.String())
	}

	root2, stdout2, _ := newTestCIRoot(t, homeDir)
	root2.SetArgs([]string{"ci", "status"})
	if _, err := root2.ExecuteC(); err != nil {
		t.Fatalf("ci status: %v", err)
	}
	if !strings.Contains(stdout2.String(), "local") {
		t.Errorf("ci status output = %q, want it to show source \"local\"", stdout2.String())
	}
}

// TestCICmd_Run_FailingStepExitsNonZero proves a failing step (the "test"
// phase configured to "false") makes `ci run` return a non-nil error --
// main.go's ExitCode(err) maps that to a non-zero process exit.
func TestCICmd_Run_FailingStepExitsNonZero(t *testing.T) {
	homeDir := t.TempDir()
	writeCILocalConfig(t, homeDir, "false")
	repoDir := newTestCheckout(t)

	root, _, _ := newTestCIRoot(t, homeDir)
	root.SetArgs([]string{"ci", "run", "--repo", repoDir})
	if _, err := root.ExecuteC(); err == nil {
		t.Fatal("expected a non-nil error when the test step fails")
	}
}

// TestCICmd_Run_OnlyFlagRunsOneStep proves --test-only skips lint/build
// entirely -- configuring lint to an invalid command that would fail if
// it ran, and asserting the run still passes.
func TestCICmd_Run_OnlyFlagRunsOneStep(t *testing.T) {
	homeDir := t.TempDir()
	toml := "[ci.local]\nlint = [\"exit 9\"]\ntest = [\"true\"]\nbuild = [\"exit 9\"]\n"
	if err := os.WriteFile(filepath.Join(homeDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	repoDir := newTestCheckout(t)

	root, _, _ := newTestCIRoot(t, homeDir)
	root.SetArgs([]string{"ci", "run", "--repo", repoDir, "--test-only"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("ci run --test-only: %v (lint/build must not have run)", err)
	}
}

// TestCICmd_Run_RefusedForAnActionsRoutedRepo is the never-pay refusal at
// the CLI surface: a checkout whose CI the policy puts on hosted Actions
// must not get a local run recorded against it. The checkout carries a real
// `origin` remote so the policy has an "owner/repo" to route -- which is
// also what proves the git-remote lookup is wired, not just the guard.
func TestCICmd_Run_RefusedForAnActionsRoutedRepo(t *testing.T) {
	homeDir := t.TempDir()
	writeCILocalConfig(t, homeDir, "true")
	repoDir := newGitCheckoutWithOrigin(t, "https://github.com/acamarata/cascade.git")

	root, _, _ := newTestCIRootWithRoutes(t, homeDir, allowActionsRoutes)
	root.SetArgs([]string{"ci", "run", "--repo", repoDir})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("expected `ci run` to refuse a repo whose CI routes to hosted Actions")
	}
	if !strings.Contains(err.Error(), string(RouteGitHubActions)) {
		t.Errorf("error = %q, want it to name the %q route", err.Error(), RouteGitHubActions)
	}
	assertKind(t, err, cascade.KindPolicyDenied)
}

// TestCICmd_Run_NoResolverFailsClosed proves the fail-closed default at the
// CLI surface: with no policy resolver injected, `ci run` refuses rather
// than assuming the local gate is fine.
func TestCICmd_Run_NoResolverFailsClosed(t *testing.T) {
	homeDir := t.TempDir()
	writeCILocalConfig(t, homeDir, "true")
	repoDir := newTestCheckout(t)

	root, _, _ := newTestCIRootWithRoutes(t, homeDir, nil)
	root.SetArgs([]string{"ci", "run", "--repo", repoDir})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("expected `ci run` to refuse with no never-pay policy resolver configured")
	}
	assertKind(t, err, cascade.KindUnavailable)
}

// TestCICmd_Run_RejectsANonCheckoutRepo proves --repo validation: a
// directory with no .git is refused before any step runs.
func TestCICmd_Run_RejectsANonCheckoutRepo(t *testing.T) {
	homeDir := t.TempDir()
	writeCILocalConfig(t, homeDir, "true")

	root, _, _ := newTestCIRoot(t, homeDir)
	root.SetArgs([]string{"ci", "run", "--repo", t.TempDir()})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("expected `ci run` to refuse a --repo that is not a repository checkout")
	}
	if !strings.Contains(err.Error(), ".git") {
		t.Errorf("error = %q, want it to say what is missing", err.Error())
	}
}

// TestCICmd_Status_HasNoLimitFlag proves the contract's "exactly these base
// flags" for status: the invented --limit is gone, and its absence is
// asserted rather than assumed.
func TestCICmd_Status_HasNoLimitFlag(t *testing.T) {
	statusCmd := newCIStatusCmd(CmdDeps{})
	if statusCmd.Flags().Lookup("limit") != nil {
		t.Error("`ci status` declares a --limit flag; the contract names no flags for it")
	}
}

// newGitCheckoutWithOrigin makes a real git repository with an `origin`
// remote, so ownerRepoFor's `git remote get-url origin` lookup has
// something true to read. It uses the real git binary -- the same Art.2
// external contract internal/jobs and internal/retrieval already exercise
// for real -- and skips if git is absent rather than asserting against a
// fake remote this code path would never see in production.
func newGitCheckoutWithOrigin(t *testing.T, originURL string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; the remote lookup cannot be exercised for real")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", originURL},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = AllowedEnv(testEnviron(), nil)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}
