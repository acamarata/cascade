// Purpose: proves github_ci_watch_cmd.go's `watch add|list|remove` verbs
// through the REAL cobra commands (a TempDir config.toml, CASCADE_HOME/
// CASCADE_CONFIG-scoped so tests never touch the operator's real
// ~/.cascade -- Art.7.1), plus probeAttentionDaemonAvailable,
// openAttentionPusher/newCIAttentionPusher over a TempDir sqlite store,
// waitAndRoute, and routeWaitFailureToAttention's own two guards (a
// non-KindConflict error, and a genuine routable failure landing in the
// real supervision store).
//
// Constraints: every test sets CASCADE_HOME/CASCADE_CONFIG to its own
// t.TempDir() (never the shared TestMain-redirected $HOME), so tests are
// independent of each other and of execution order.
//
// SPORT: cmd/cascade:github-ci-watch-verbs (TEST) -- P1-E25-W5-S51-T4.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// watchTestConfig points lazyPaths{} (the real path resolver every
// production call site here uses -- it takes no injected paths) at a
// fresh, empty TempDir for the duration of one test.
func watchTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CASCADE_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	t.Setenv("CASCADE_CONFIG", path)
	return path
}

func TestGitHubCIWatchAddListRemove_RoundTrip(t *testing.T) {
	watchTestConfig(t)

	add := newGitHubCIWatchAddCmd()
	var addOut strings.Builder
	add.SetOut(&addOut)
	add.SetArgs([]string{"acamarata/cascade", "--branch", "main", "--workflow", "ci-*"})
	if err := add.Execute(); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(addOut.String(), "added watch for acamarata/cascade") {
		t.Errorf("add output = %q, want an \"added\" line", addOut.String())
	}

	list := newGitHubCIWatchListCmd()
	var listOut strings.Builder
	list.SetOut(&listOut)
	if err := list.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listOut.String(), "acamarata/cascade") || !strings.Contains(listOut.String(), `branch="main"`) {
		t.Errorf("list output = %q, want the watched repo with its branch", listOut.String())
	}

	remove := newGitHubCIWatchRemoveCmd()
	var removeOut strings.Builder
	remove.SetOut(&removeOut)
	remove.SetArgs([]string{"acamarata/cascade"})
	if err := remove.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(removeOut.String(), "removed watch for acamarata/cascade") {
		t.Errorf("remove output = %q, want a \"removed\" line", removeOut.String())
	}

	list2 := newGitHubCIWatchListCmd()
	var listOut2 strings.Builder
	list2.SetOut(&listOut2)
	if err := list2.Execute(); err != nil {
		t.Fatalf("list (after remove): %v", err)
	}
	if !strings.Contains(listOut2.String(), "no watched repositories") {
		t.Errorf("list output after remove = %q, want the empty message", listOut2.String())
	}
}

// TestGitHubCIWatchAdd_IsIdempotentOnRepo proves AC4: a second add for the
// same repo UPDATES the entry (branch/workflow) rather than appending a
// duplicate.
func TestGitHubCIWatchAdd_IsIdempotentOnRepo(t *testing.T) {
	watchTestConfig(t)

	first := newGitHubCIWatchAddCmd()
	first.SetOut(&strings.Builder{})
	first.SetArgs([]string{"acamarata/cascade"})
	if err := first.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}

	second := newGitHubCIWatchAddCmd()
	var secondOut strings.Builder
	second.SetOut(&secondOut)
	second.SetArgs([]string{"acamarata/cascade", "--branch", "dev"})
	if err := second.Execute(); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !strings.Contains(secondOut.String(), "updated watch for acamarata/cascade") {
		t.Errorf("second add output = %q, want an \"updated\" line", secondOut.String())
	}

	list := newGitHubCIWatchListCmd()
	var listOut strings.Builder
	list.SetOut(&listOut)
	if err := list.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Count(listOut.String(), "acamarata/cascade") != 1 {
		t.Fatalf("list output = %q, want exactly one entry (updated in place, not duplicated)", listOut.String())
	}
	if !strings.Contains(listOut.String(), `branch="dev"`) {
		t.Errorf("list output = %q, want the updated branch", listOut.String())
	}
}

// TestGitHubCIWatchRemove_AbsentRepoIsANoOp proves removing a repo that
// was never watched succeeds (exit nil) and reports the no-op, rather
// than erroring.
func TestGitHubCIWatchRemove_AbsentRepoIsANoOp(t *testing.T) {
	watchTestConfig(t)

	remove := newGitHubCIWatchRemoveCmd()
	var out strings.Builder
	remove.SetOut(&out)
	remove.SetArgs([]string{"acamarata/never-watched"})
	if err := remove.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out.String(), "no-op: acamarata/never-watched was not watched") {
		t.Errorf("output = %q, want the no-op line", out.String())
	}
}

// TestGitHubCIWatchAdd_RefusesAnArrayOfTablesConfig proves the write-side
// [[ci.watch]] refusal (config_ci_watch_write.go's refuseCIWatchTableArray)
// surfaces through the cobra RunE, not just the runtime package's own
// direct-call tests.
func TestGitHubCIWatchAdd_RefusesAnArrayOfTablesConfig(t *testing.T) {
	path := watchTestConfig(t)
	if err := os.WriteFile(path, []byte("[[ci.watch]]\nrepo = \"a/b\"\n"), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	add := newGitHubCIWatchAddCmd()
	add.SetOut(&strings.Builder{})
	add.SetArgs([]string{"acamarata/cascade"})
	err := add.Execute()
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput (the [[ci.watch]] refusal)", err)
	}
	if !strings.Contains(err.Error(), "array of tables") {
		t.Errorf("err = %q, want it to name the array-of-tables refusal", err.Error())
	}
}

// TestProbeAttentionDaemonAvailable_NilOnThisPlatform proves the AC6 gate
// is a no-op on a tier-1 (non-Windows) host -- the only platform this
// suite runs on.
func TestProbeAttentionDaemonAvailable_NilOnThisPlatform(t *testing.T) {
	if err := probeAttentionDaemonAvailable(); err != nil {
		t.Fatalf("probeAttentionDaemonAvailable() = %v, want nil on this (non-Windows) host", err)
	}
}

// TestOpenAttentionPusher_OpensAndClosesOverATempDirStore proves the CLI
// path's store construction (the SAME openMCPPolicyStore call
// hostMergeOnGreen's policy engine uses) succeeds and yields a usable
// pusher, closed here BEFORE t.TempDir's own cleanup (Art.7.1).
func TestOpenAttentionPusher_OpensAndClosesOverATempDirStore(t *testing.T) {
	watchTestConfig(t)

	pusher, closeStore, err := openAttentionPusher([]string{"acamarata/*"})
	if err != nil {
		t.Fatalf("openAttentionPusher: %v", err)
	}
	if pusher == nil {
		t.Fatal("pusher = nil, want a non-nil ci.AttentionPusher")
	}
	closeStore()
}

// TestNewCIAttentionPusher_NilStoreIsATypedError proves the construction
// failure branch: daemon.NewAttentionStore(nil, ...) returns nil, and
// newCIAttentionPusher turns that into a typed KindInternal error rather
// than handing back a pusher that would nil-pointer-panic on first use.
func TestNewCIAttentionPusher_NilStoreIsATypedError(t *testing.T) {
	_, err := newCIAttentionPusher(nil, runtime.NewSystemClock(), nil, nil)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want KindInternal", err)
	}
}

// TestWaitAndRoute_ReturnsWaitOnGreensErrorAndNeverPanics proves the ONE
// call site both hostWaitOnGreen and hostMergeOnGreen share: it calls
// ci.WaitOnGreen, folds the result through routeWaitFailureToAttention
// (whose own guard is proven separately below), and returns WaitOnGreen's
// result/error untouched. An invalid WaitDeps (no Client) is deterministic
// and offline -- ci.WaitOnGreen refuses before any network attempt.
func TestWaitAndRoute_ReturnsWaitOnGreensErrorAndNeverPanics(t *testing.T) {
	_, err := waitAndRoute(context.Background(), ci.WaitDeps{},
		ci.WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput (WaitDeps.validate's nil-Client refusal)", err)
	}
}

// TestRouteWaitFailureToAttention_IgnoresNonRoutableErrors proves the
// kind guard: neither a nil error (the success path) nor a non-KindConflict
// error (timeout, cancellation) ever reaches Load/openAttentionPusher --
// exercised here by pointing CASCADE_CONFIG at a path whose PARENT
// directory does not exist, so a Load attempt would fail loudly were the
// guard ever removed.
func TestRouteWaitFailureToAttention_IgnoresNonRoutableErrors(t *testing.T) {
	t.Setenv("CASCADE_CONFIG", filepath.Join(t.TempDir(), "missing-parent", "config.toml"))
	result := ci.WaitResult{Owner: "acamarata", Repo: "cascade", Ref: "main", RunID: 1}

	for name, waitErr := range map[string]error{
		"nil (success path)":             nil,
		"KindTimeout (not KindConflict)": cascade.New(cascade.KindTimeout, "ci: wait-on-green timed out"),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("routeWaitFailureToAttention panicked: %v", r)
				}
			}()
			routeWaitFailureToAttention(context.Background(), result, waitErr)
		})
	}
}

// TestRouteWaitFailureToAttention_RoutesAMatchingFailureIntoTheRealStore
// is this file's end-to-end proof for the operator-driven half of
// P1-E25-W5-S51-T4's routing core (internal/ci/attention.go's header
// names this the "operator-driven path"): a genuine KindConflict failure
// for a watched repo, routed through the real cobra-free call
// routeWaitFailureToAttention makes in production, lands in a real
// TempDir supervision store -- reopened here to verify, the same way
// daemon_unix_ci_attention_test.go's wiring proof verifies the bus path.
func TestRouteWaitFailureToAttention_RoutesAMatchingFailureIntoTheRealStore(t *testing.T) {
	path := watchTestConfig(t)
	if err := os.WriteFile(path, []byte(`ci.watch = [{repo = "acamarata/cascade"}]`+"\n"), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	result := ci.WaitResult{
		Owner: "acamarata", Repo: "cascade", Ref: "main", RunID: 909,
		Conclusion: ci.ConclusionFailure, WorkflowName: "ci",
	}
	waitErr := cascade.New(cascade.KindConflict, "ci: run 909 concluded failure")

	routeWaitFailureToAttention(context.Background(), result, waitErr)

	clock := runtime.NewSystemClock()
	store, closeStore, err := openMCPPolicyStore(lazyPaths{}, clock)
	if err != nil {
		t.Fatalf("openMCPPolicyStore (verify): %v", err)
	}
	defer closeStore()
	verify := daemon.NewAttentionStore(store, clock, nil)
	items, err := verify.ListInScopes(context.Background(),
		[]supervision.ScopeRef{{Kind: scope.ScopeKindGlobal}}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("global-scope items = %d, want exactly 1", len(items))
	}
	if want := "ci:acamarata/cascade:909"; items[0].SourceRef != want {
		t.Errorf("SourceRef = %q, want %q", items[0].SourceRef, want)
	}
}
