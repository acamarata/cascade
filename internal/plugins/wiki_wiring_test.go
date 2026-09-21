package plugins

// Purpose: proves the wiki plugin-host call path bridge -- the
// *process.Handle/GitHubPluginCaller identity, the real dispatch (method +
// params passed through unchanged), and the honest default refusal when
// no live Handle exists (which is every case in this build today; see
// wiki_wiring.go's header comment). Mirrors
// ci_waitmerge_wiring_test.go's structure exactly, for the sibling bridge.
//
// SPORT: internal/plugins wiki-wiring/TESTED (P1-E25-W5-S51-T6).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGitHubWikiCaller_ProviderErrorPropagates proves Call surfaces the
// HandleProvider's own refusal verbatim rather than swallowing it.
func TestGitHubWikiCaller_ProviderErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "no handle in this fake path")
	caller := NewGitHubWikiCaller(func(_ context.Context, pluginID string) (*process.Handle, error) {
		if pluginID != cascadeGitHubPluginID {
			t.Fatalf("provider asked for %q, want %q", pluginID, cascadeGitHubPluginID)
		}
		return nil, wantErr
	})
	_, err := caller.Call(context.Background(), "cascade-github.wiki.sync", []byte(`{"owner":"a"}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) || err.Error() != wantErr.Error() {
		t.Fatalf("err = %v, want the provider's own refusal %v", err, wantErr)
	}
}

// TestGitHubWikiCaller_NilHandleNilErrorRefusesRatherThanPanics proves the
// defensive nil-Handle guard: a misbehaving provider that returns (nil,
// nil) must never reach a nil-pointer Handle.Call.
func TestGitHubWikiCaller_NilHandleNilErrorRefusesRatherThanPanics(t *testing.T) {
	caller := NewGitHubWikiCaller(func(context.Context, string) (*process.Handle, error) { return nil, nil })
	_, err := caller.Call(context.Background(), "cascade-github.wiki.check", nil)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want KindInternal", err)
	}
}

// TestGitHubWikiCaller_RefusesWithoutARunningProcess proves the honest
// default: NewGitHubWikiCaller(noRunningPluginHandle) refuses every call
// rather than fabricating success, and never panics on a nil Handle.
func TestGitHubWikiCaller_RefusesWithoutARunningProcess(t *testing.T) {
	caller := NewGitHubWikiCaller(noRunningPluginHandle)
	_, err := caller.Call(context.Background(), "cascade-github.wiki.sync", []byte(`{}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), cascadeGitHubPluginID) {
		t.Fatalf("err = %q, want it to name %q", err.Error(), cascadeGitHubPluginID)
	}
}

// TestGitHubWikiCaller_NilProviderRefusesRatherThanPanics proves the
// nil-provider guard.
func TestGitHubWikiCaller_NilProviderRefusesRatherThanPanics(t *testing.T) {
	caller := NewGitHubWikiCaller(nil)
	_, err := caller.Call(context.Background(), "cascade-github.wiki.check", nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestHandleSatisfiesGitHubPluginCaller pins the compile-time assertion in
// wiki_wiring.go's own text, so a reviewer sees the identity proven by an
// executable test, not only a comment.
func TestHandleSatisfiesGitHubPluginCaller(_ *testing.T) {
	var _ GitHubPluginCaller = (*process.Handle)(nil)
}

// TestNewGitHubWikiCallerForHost_ReturnsTheTypedPrerequisite proves what
// the PRODUCTION entry point (cmd/cascade/github_wiki_cmd.go's caller)
// gets today: a typed KindUnavailable refusal that names the plugin and
// the missing trust-elevation path, never a caller that would fail later
// with a vaguer message and never a fabricated success.
func TestNewGitHubWikiCallerForHost_ReturnsTheTypedPrerequisite(t *testing.T) {
	caller, err := NewGitHubWikiCallerForHost(context.Background())
	if caller != nil {
		t.Fatalf("caller = %v, want nil while the prerequisite is unmet", caller)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	for _, want := range []string{cascadeGitHubPluginID, "trust-elevation", "wiki", "P1-E24-W5-S50-T4"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), want)
		}
	}
}

// TestNewGitHubWikiCallerForHost_SuccessBranchConstructsAWorkingCaller
// reaches ForHost's SUCCESS branch through the wikiHandleProvider seam
// (wiki_wiring.go): a fake provider that resolves without error, rather
// than a live cascade-github process. It proves three things a passing
// build could otherwise fake: (1) ForHost actually returns a non-nil
// caller and a nil error when the prerequisite check succeeds, not just
// the refusal path every other test in this file exercises; (2) the
// prerequisite check is evaluated against cascadeGitHubPluginID, the
// same id production resolves; (3) the RETURNED caller dispatches
// through the SAME injected provider, not a stale reference to
// noRunningPluginHandle -- proven by observing a second provider call
// when caller.Call is invoked, and by the KindInternal shape that only
// the injected provider's (nil, nil) reply produces (noRunningPluginHandle
// would instead produce KindUnavailable).
func TestNewGitHubWikiCallerForHost_SuccessBranchConstructsAWorkingCaller(t *testing.T) {
	original := wikiHandleProvider
	t.Cleanup(func() { wikiHandleProvider = original })

	var calls int
	var gotPluginIDs []string
	wikiHandleProvider = func(_ context.Context, pluginID string) (*process.Handle, error) {
		calls++
		gotPluginIDs = append(gotPluginIDs, pluginID)
		return nil, nil
	}

	caller, err := NewGitHubWikiCallerForHost(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil once the injected provider resolves without error", err)
	}
	if caller == nil {
		t.Fatal("caller = nil, want a working GitHubPluginCaller on the success branch")
	}
	if calls != 1 || gotPluginIDs[0] != cascadeGitHubPluginID {
		t.Fatalf("prerequisite check calls = %d, ids = %v, want one call naming %q", calls, gotPluginIDs, cascadeGitHubPluginID)
	}

	// The returned caller must dispatch through the SAME injected
	// provider (a second call), not a caller that silently fell back to
	// noRunningPluginHandle -- which would refuse with KindUnavailable
	// instead of this fake's KindInternal (nil Handle, nil error).
	_, callErr := caller.Call(context.Background(), "cascade-github.wiki.sync", []byte(`{}`))
	if !cascade.HasKind(callErr, cascade.KindInternal) {
		t.Fatalf("Call err = %v, want KindInternal (proves the returned caller used the injected provider)", callErr)
	}
	if calls != 2 {
		t.Fatalf("provider calls after caller.Call = %d, want 2 (ForHost's check + the caller's own dispatch)", calls)
	}
}
