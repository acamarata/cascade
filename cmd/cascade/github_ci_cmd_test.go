// Purpose: proves the two host-implemented verbs -- that wait's RunE parses
// its flags and reaches the internal/ci bridge (never the plugin process),
// that the PRODUCTION wait path reaches internal/ci's own composition, and
// that merge-on-green refuses today with the typed elevation prerequisite.
//
// Constraints: no network and no keychain. Every test either injects the
// bridge or clears the token variables so the production path refuses in
// internal/ci before a socket is opened.
//
// SPORT: cmd/cascade:github-ci-verbs (TESTED) -- P1-E25-W5-S51-T3.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clearGitHubTokenEnv makes the production wait path deterministic and
// offline: with no token, ci.WaitDepsFromEnv refuses before any request.
func clearGitHubTokenEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(name, "")
	}
}

// TestGitHubCIWaitReachesTheHostBridgeWithItsParsedFlags proves the RunE
// reaches internal/ci (the host bridge), with exactly the options the flags
// named -- and that it reaches nothing else.
func TestGitHubCIWaitReachesTheHostBridgeWithItsParsedFlags(t *testing.T) {
	var got ci.WaitOptions
	calls := 0
	cmd := newGitHubCIWaitCmd(githubCIBridge{
		Wait: func(_ context.Context, opts ci.WaitOptions) (ci.WaitResult, error) {
			calls++
			got = opts
			return ci.WaitResult{Owner: opts.Owner, Repo: opts.Repo, Ref: opts.Ref, RunID: 501, Passed: true}, nil
		},
		Merge: func(context.Context, ci.MergeOptions) (ci.MergeResult, error) {
			t.Fatal("wait must never reach the merge bridge")
			return ci.MergeResult{}, nil
		},
	})
	cmd.SetArgs([]string{
		"--repo", "acamarata/cascade", "--ref", "main", "--timeout", "90s",
		"--required-check", "build", "--required-check", "lint", "--allow-skipped", "docs",
	})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 1 {
		t.Fatalf("bridge calls = %d, want 1", calls)
	}
	want := ci.WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", Timeout: 90 * time.Second,
		RequiredChecks: []string{"build", "lint"}, AllowSkipped: []string{"docs"},
	}
	if got.Owner != want.Owner || got.Repo != want.Repo || got.Ref != want.Ref || got.Timeout != want.Timeout {
		t.Fatalf("opts = %+v, want %+v", got, want)
	}
	if strings.Join(got.RequiredChecks, ",") != "build,lint" || strings.Join(got.AllowSkipped, ",") != "docs" {
		t.Fatalf("check lists = %v / %v, want [build lint] / [docs]", got.RequiredChecks, got.AllowSkipped)
	}
}

// TestGitHubCIWaitRefusesABadRepoFlag proves the flag is parsed, not
// guessed.
func TestGitHubCIWaitRefusesABadRepoFlag(t *testing.T) {
	cmd := newGitHubCIWaitCmd(githubCIBridge{
		Wait: func(context.Context, ci.WaitOptions) (ci.WaitResult, error) {
			t.Fatal("the bridge must not be reached with an unparseable --repo")
			return ci.WaitResult{}, nil
		},
	})
	cmd.SetArgs([]string{"--repo", "not-a-repo", "--ref", "main"})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestGitHubCIWaitProductionPathReachesInternalCI runs the REAL mounted
// command with no injected bridge at all. The refusal it returns is
// internal/ci's own (ci.ErrNoGitHubToken), which is only reachable through
// ci.WaitDepsFromEnv -- so the production RunE demonstrably reaches
// internal/ci, and not the cascade-github process.
func TestGitHubCIWaitProductionPathReachesInternalCI(t *testing.T) {
	clearGitHubTokenEnv(t)
	_, err := execRoot(t, "github", "ci", "wait", "--repo", "acamarata/cascade", "--ref", "main")
	if err == nil {
		t.Fatal("err = nil, want internal/ci's no-token refusal")
	}
	if err.Error() != ci.ErrNoGitHubToken().Error() {
		t.Fatalf("err = %q, want ci.ErrNoGitHubToken()'s exact text", err.Error())
	}
}

// TestGitHubCIMergeOnGreenReturnsTheElevationPrerequisite proves what a user
// gets today: the typed refusal naming the missing trust-elevation path, not
// a silent failure and not a pretend merge.
func TestGitHubCIMergeOnGreenReturnsTheElevationPrerequisite(t *testing.T) {
	clearGitHubTokenEnv(t)
	_, err := execRoot(t, "github", "ci", "merge-on-green",
		"--repo", "acamarata/cascade", "--ref", "main", "--pr", "42", "--yes")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	for _, want := range []string{"merge-on-green", "cascade-github", "trust-elevation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), want)
		}
	}
}

// TestGitHubCIMergeOnGreenRequiresYes proves an L3 external side effect
// never runs off a defaulted flag.
func TestGitHubCIMergeOnGreenRequiresYes(t *testing.T) {
	cmd := newGitHubCIMergeOnGreenCmd(githubCIBridge{
		Merge: func(context.Context, ci.MergeOptions) (ci.MergeResult, error) {
			t.Fatal("merge must not be attempted without --yes")
			return ci.MergeResult{}, nil
		},
	})
	cmd.SetArgs([]string{"--repo", "acamarata/cascade", "--ref", "main", "--pr", "42"})
	cmd.SetOut(&strings.Builder{})
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %q, want it to name --yes", err.Error())
	}
}

// TestGitHubCIMergeOnGreenReachesTheBridgeWithItsParsedFlags proves the
// merge RunE's own parsing and bridge hand-off.
func TestGitHubCIMergeOnGreenReachesTheBridgeWithItsParsedFlags(t *testing.T) {
	var got ci.MergeOptions
	cmd := newGitHubCIMergeOnGreenCmd(githubCIBridge{
		Merge: func(_ context.Context, opts ci.MergeOptions) (ci.MergeResult, error) {
			got = opts
			return ci.MergeResult{Merged: true, SHA: "cafef00d"}, nil
		},
	})
	cmd.SetArgs([]string{"--repo", "acamarata/cascade", "--ref", "main", "--pr", "42", "--yes"})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Owner != "acamarata" || got.Repo != "cascade" || got.PR != 42 || got.Ref != "main" {
		t.Fatalf("opts = %+v, want the parsed repo/ref/pr", got)
	}
	if got.Subject != githubCISubject {
		t.Fatalf("Subject = %+v, want the CLI's declared subject", got.Subject)
	}
}

// TestGitHubCICapabilitiesAreDistinctAndExternal proves the registration
// D9 requires: two DIFFERENT capability names, both at the class that puts
// them on the L3 rung.
func TestGitHubCICapabilitiesAreDistinctAndExternal(t *testing.T) {
	caps := githubCICapabilities()
	if len(caps) != 2 {
		t.Fatalf("got %d capabilities, want 2", len(caps))
	}
	if caps[0].Name == caps[1].Name {
		t.Fatalf("both capabilities are named %q: an unattended merge must not be satisfied by a human-merge grant", caps[0].Name)
	}
	for _, c := range caps {
		if c.Class() != policy.ClassExternalSideEffect {
			t.Fatalf("%s class = %v, want the external-side-effect class (L3)", c.Name, c.Class())
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s does not validate: %v", c.Name, err)
		}
	}
	if caps[1].Name != ci.MergeOnGreenCapability {
		t.Fatalf("merge-on-green capability = %q, want %q", caps[1].Name, ci.MergeOnGreenCapability)
	}
}
