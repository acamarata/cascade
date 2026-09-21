package plugins

// Purpose: proves the plugin-host call path bridge -- the
// *process.Handle/ci.MergeCaller identity, the real dispatch (method +
// params passed through unchanged), and the honest default refusal when
// no live Handle exists (which is every case in this build today; see
// ci_waitmerge_wiring.go's header comment).
//
// SPORT: internal/plugins ci-waitmerge-wiring/TESTED (P1-E25-W5-S51-T3).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGitHubMergeCaller_ProviderErrorPropagates proves Call surfaces the
// HandleProvider's own refusal verbatim rather than swallowing it -- the
// only path exercisable without a real spawned subprocess (process.Handle
// has no exported constructor outside a genuine Launch; process's own
// package proves Handle.Call's transport forwarding).
func TestGitHubMergeCaller_ProviderErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "no handle in this fake path")
	caller := NewGitHubMergeCaller(func(_ context.Context, pluginID string) (*process.Handle, error) {
		if pluginID != cascadeGitHubPluginID {
			t.Fatalf("provider asked for %q, want %q", pluginID, cascadeGitHubPluginID)
		}
		return nil, wantErr
	})
	_, err := caller.Call(context.Background(), "cascade-github.prs.merge", []byte(`{"number":1}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) || err.Error() != wantErr.Error() {
		t.Fatalf("err = %v, want the provider's own refusal %v", err, wantErr)
	}
}

// TestGitHubMergeCaller_NilHandleNilErrorRefusesRatherThanPanics proves the
// defensive nil-Handle guard: a misbehaving provider that returns (nil,
// nil) must never reach a nil-pointer Handle.Call.
func TestGitHubMergeCaller_NilHandleNilErrorRefusesRatherThanPanics(t *testing.T) {
	caller := NewGitHubMergeCaller(func(context.Context, string) (*process.Handle, error) { return nil, nil })
	_, err := caller.Call(context.Background(), "cascade-github.prs.merge", nil)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want KindInternal", err)
	}
}

// TestGitHubMergeCaller_RefusesWithoutARunningProcess proves the honest
// default: NewGitHubMergeCaller(noRunningPluginHandle) refuses every call
// rather than fabricating success, and never panics on a nil Handle.
func TestGitHubMergeCaller_RefusesWithoutARunningProcess(t *testing.T) {
	caller := NewGitHubMergeCaller(noRunningPluginHandle)
	_, err := caller.Call(context.Background(), "cascade-github.prs.merge", []byte(`{}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), cascadeGitHubPluginID) {
		t.Fatalf("err = %q, want it to name %q", err.Error(), cascadeGitHubPluginID)
	}
}

// TestGitHubMergeCaller_NilProviderRefusesRatherThanPanics proves the
// nil-provider guard.
func TestGitHubMergeCaller_NilProviderRefusesRatherThanPanics(t *testing.T) {
	caller := NewGitHubMergeCaller(nil)
	_, err := caller.Call(context.Background(), "cascade-github.prs.merge", nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestHandleSatisfiesMergeCaller pins the compile-time assertion in
// ci_waitmerge_wiring.go's own text, so a reviewer sees the identity
// proven by an executable test, not only a comment.
func TestHandleSatisfiesMergeCaller(_ *testing.T) {
	var _ ci.MergeCaller = (*process.Handle)(nil)
}

// TestNewGitHubMergeCallerForHost_ReturnsTheTypedPrerequisite proves what
// the PRODUCTION entry point (cmd/cascade/github_ci_cmd.go's caller) gets
// today: a typed KindUnavailable refusal that names the plugin and the
// missing trust-elevation path, never a caller that would fail later with a
// vaguer message and never a fabricated success.
func TestNewGitHubMergeCallerForHost_ReturnsTheTypedPrerequisite(t *testing.T) {
	caller, err := NewGitHubMergeCallerForHost(context.Background())
	if caller != nil {
		t.Fatalf("caller = %v, want nil while the prerequisite is unmet", caller)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	// "P1-E24-W5-S50-T4": the confirming review's note 6 -- the refusal
	// named the MECHANISM (ProvisionElevated, the trust gate) but not the
	// ticket that delivers it, so a reader had nowhere to go next.
	for _, want := range []string{cascadeGitHubPluginID, "trust-elevation", "merge-on-green", "P1-E24-W5-S50-T4"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), want)
		}
	}
}
