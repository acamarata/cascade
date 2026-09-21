// Purpose: proves the two host-mounted wiki verbs -- that sync/check's
// RunE parses its flags and reaches the injected resolver (never a real
// process), and that the PRODUCTION path (execRoot, no injected
// resolver) reaches internal/plugins' honest "no trust-elevation path"
// refusal, the same typed prerequisite S-51.T3's merge-on-green already
// returns for its own single process call.
//
// Constraints: no network and no live plugin process. Every test either
// injects the resolver or drives the real mounted command, whose real
// resolver (plugins.NewGitHubWikiCallerForHost) refuses before any
// transport is touched.
//
// SPORT: cmd/cascade:github-wiki-verbs (TESTED) -- P1-E25-W5-S51-T6.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/wiki"
)

// TestGitHubWikiSync_ReachesTheResolverWithItsParsedFlags proves the RunE
// reaches the injected resolver with exactly the flags it parsed, and
// dispatches the exact RPC method/params wikiReply expects.
func TestGitHubWikiSync_ReachesTheResolverWithItsParsedFlags(t *testing.T) {
	calls := 0
	var gotMethod string
	var gotArgs githubWikiArgs
	resolve := func(context.Context) (githubWikiCaller, error) {
		return fakeWikiCaller{fn: func(_ context.Context, method string, params []byte) ([]byte, error) {
			calls++
			gotMethod = method
			if err := json.Unmarshal(params, &gotArgs); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			res, _ := json.Marshal(wiki.SyncResult{NoOp: true, Reason: "test"})
			return res, nil
		}}, nil
	}
	cmd := newGitHubWikiSyncCmd(resolve)
	cmd.SetArgs([]string{"--owner", "acamarata", "--repo", "cascade", "--local-dir", "docs/wiki", "--yes"})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 1 {
		t.Fatalf("resolver-dispatched calls = %d, want 1", calls)
	}
	if gotMethod != "cascade-github.wiki.sync" {
		t.Fatalf("method = %q, want cascade-github.wiki.sync", gotMethod)
	}
	want := githubWikiArgs{Owner: "acamarata", Repo: "cascade", LocalDir: "docs/wiki", Yes: true}
	if gotArgs != want {
		t.Fatalf("args = %+v, want %+v", gotArgs, want)
	}
}

// TestGitHubWikiSync_RequiresOwnerAndRepo proves the flags are validated
// before the resolver is ever reached.
func TestGitHubWikiSync_RequiresOwnerAndRepo(t *testing.T) {
	cmd := newGitHubWikiSyncCmd(func(context.Context) (githubWikiCaller, error) {
		t.Fatal("the resolver must not be reached without --owner/--repo")
		return nil, nil
	})
	cmd.SetArgs([]string{"--yes"})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestGitHubWikiCheck_ReturnsAConflictErrorOnDrift proves AC5: a non-clean
// drift report is a non-zero exit (a typed KindConflict error), not a
// silent success.
func TestGitHubWikiCheck_ReturnsAConflictErrorOnDrift(t *testing.T) {
	resolve := func(context.Context) (githubWikiCaller, error) {
		return fakeWikiCaller{fn: func(context.Context, string, []byte) ([]byte, error) {
			res, _ := json.Marshal(githubWikiCheckWireResult{
				DriftResult: wiki.DriftResult{Added: []string{"New.md"}, Clean: false},
				ExitCode:    1,
			})
			return res, nil
		}}, nil
	}
	cmd := newGitHubWikiCheckCmd(resolve)
	cmd.SetArgs([]string{"--owner", "acamarata", "--repo", "cascade"})
	out := &strings.Builder{}
	cmd.SetOut(out)
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict", err)
	}
	if !strings.Contains(out.String(), "New.md") {
		t.Fatalf("output = %q, want it to report the drifted file", out.String())
	}
}

// TestGitHubWikiCheck_CleanReturnsNil proves the AC5 zero-exit half.
func TestGitHubWikiCheck_CleanReturnsNil(t *testing.T) {
	resolve := func(context.Context) (githubWikiCaller, error) {
		return fakeWikiCaller{fn: func(context.Context, string, []byte) ([]byte, error) {
			res, _ := json.Marshal(githubWikiCheckWireResult{DriftResult: wiki.DriftResult{Clean: true}, ExitCode: 0})
			return res, nil
		}}, nil
	}
	cmd := newGitHubWikiCheckCmd(resolve)
	cmd.SetArgs([]string{"--owner", "acamarata", "--repo", "cascade"})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v, want nil (no drift)", err)
	}
}

// TestGitHubWikiSync_ProductionPathReachesTheHonestPrerequisite runs the
// REAL mounted command with no injected resolver: the refusal it returns
// is internal/plugins' own (NewGitHubWikiCallerForHost), reachable only
// through the real plugin-host bridge -- so the production RunE
// demonstrably reaches internal/plugins, and never a fabricated success.
// This is the built-binary proof PRE-RULING 1 asked for, driven through
// the same execRoot helper github_ci_cmd_test.go's identical assertion
// uses for merge-on-green.
func TestGitHubWikiSync_ProductionPathReachesTheHonestPrerequisite(t *testing.T) {
	_, err := execRoot(t, "github", "wiki", "sync", "--owner", "acamarata", "--repo", "cascade", "--yes")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	for _, want := range []string{"cascade-github", "trust-elevation", "wiki.sync"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), want)
		}
	}
}

// TestGitHubWikiCheck_ProductionPathReachesTheHonestPrerequisite is the
// same proof for `wiki check`.
func TestGitHubWikiCheck_ProductionPathReachesTheHonestPrerequisite(t *testing.T) {
	_, err := execRoot(t, "github", "wiki", "check", "--owner", "acamarata", "--repo", "cascade")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "cascade-github") {
		t.Fatalf("err = %q, want it to name cascade-github", err.Error())
	}
}

// fakeWikiCaller is a scripted githubWikiCaller.
type fakeWikiCaller struct {
	fn func(ctx context.Context, method string, params []byte) ([]byte, error)
}

func (f fakeWikiCaller) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	return f.fn(ctx, method, params)
}
