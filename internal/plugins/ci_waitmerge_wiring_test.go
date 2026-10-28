//go:build !windows

package plugins

// Purpose: proves the merge-on-green bridge: the provider's refusal is
// surfaced verbatim, a nil provider or Handle refuses, and the PRODUCTION
// entry point reaches a live, recorded cascade-github TrustedHandle once a
// witnessed plugin.add trusted it, and refuses naming
// `cascade plugin add cascade-github` before.
// SPORT: internal/plugins ci-waitmerge-wiring/TESTED (P1-E25-W5-S51-T3; P1-PLG-03).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unknownMethod is answered by cascade-github's dispatch with a typed
// "no such method" error, before any tool or egress code runs.
const unknownMethod = "nosuch.method"

func TestGitHubMergeCaller_ProviderErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "no handle in this fake path")
	caller := NewGitHubMergeCaller(func(_ context.Context, pluginID string) (*TrustedHandle, error) {
		if pluginID != cascadeGitHubPluginID {
			t.Fatalf("provider asked for %q, want %q", pluginID, cascadeGitHubPluginID)
		}
		return nil, wantErr
	})
	if _, err := caller.Call(context.Background(), "cascade-github.prs.merge", []byte(`{}`)); err != wantErr {
		t.Fatalf("err = %v, want the provider's own error", err)
	}
	nilHandle := NewGitHubMergeCaller(func(context.Context, string) (*TrustedHandle, error) { return nil, nil })
	if _, err := nilHandle.Call(context.Background(), "m", nil); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("nil Handle err = %v, want KindInternal", err)
	}
	if _, err := NewGitHubMergeCaller(nil).Call(context.Background(), "m", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil provider err = %v, want KindInvalidInput", err)
	}
}

// requireAddRefusal asserts the typed refusal naming the attested add.
func requireAddRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("%s err = %v, want KindUnavailable", what, err)
	}
	for _, want := range []string{"cascade plugin add cascade-github", what} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%s err = %q, want it to name %q", what, err, want)
		}
	}
}

// requireLiveReply asserts err is cascade-github's own typed reply to
// unknownMethod, and that the launch and the call were each recorded.
func requireLiveReply(t *testing.T, f *launchFix, err error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindInternal) || !strings.Contains(err.Error(), "cascade-github: no such method") {
		t.Fatalf("Call err = %v, want cascade-github's own no-such-method reply (the live handle)", err)
	}
	launches, calls := map[string]int{}, map[string]int{}
	for _, r := range f.allRecords(t) {
		switch {
		case strings.HasPrefix(r.EffectKey, launchEffectPrefix+cascadeGitHubPluginID+":"):
			launches[r.EffectPhase]++
		case strings.HasPrefix(r.EffectKey, callEffectPrefix+cascadeGitHubPluginID+":"):
			calls[r.EffectPhase]++
		}
	}
	if launches["intent"] != 1 || launches["confirmed"] != 1 || calls["intent"] != 1 || calls["failed"] != 1 {
		t.Fatalf("effect records launch=%v call=%v, want one launch intent+confirm and one call intent+failed", launches, calls)
	}
}

func TestMergeOnGreenReachesLiveHandle(t *testing.T) {
	f := newLaunchFix(t)
	f.installGitHub(t)
	ctx := context.Background()
	_, err := NewGitHubMergeCallerForHost(ctx)
	requireAddRefusal(t, err, "merge-on-green")
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	caller, err := NewGitHubMergeCallerForHost(ctx)
	if err != nil || caller == nil {
		t.Fatalf("NewGitHubMergeCallerForHost after a witnessed add = %v, %v", caller, err)
	}
	_, err = caller.Call(ctx, unknownMethod, []byte(`{}`))
	requireLiveReply(t, f, err)
	if n := len(launchedHandles()); n != 0 {
		t.Fatalf("%d launched handles left open after the call, want 0", n)
	}
}

// launchedHandles snapshots the live TrustedHandle set.
func launchedHandles() []*TrustedHandle {
	launched.mu.Lock()
	defer launched.mu.Unlock()
	out := make([]*TrustedHandle, 0, len(launched.set))
	for h := range launched.set {
		out = append(out, h)
	}
	return out
}
