//go:build !windows

package plugins

// Purpose: proves the wiki bridge: the provider's refusal is surfaced
// verbatim, and the PRODUCTION entry point reaches a live, recorded
// cascade-github TrustedHandle once a witnessed plugin.add trusted it, and
// refuses naming `cascade plugin add cascade-github` before.
// SPORT: internal/plugins wiki-wiring/TESTED (P1-E25-W5-S51-T6; P1-PLG-03).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestGitHubWikiCaller_ProviderErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "no handle in this fake path")
	caller := NewGitHubWikiCaller(func(context.Context, string) (*TrustedHandle, error) { return nil, wantErr })
	if _, err := caller.Call(context.Background(), "cascade-github.wiki.sync", []byte(`{}`)); err != wantErr {
		t.Fatalf("err = %v, want the provider's own error", err)
	}
	if _, err := NewGitHubWikiCaller(nil).Call(context.Background(), "m", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil provider err = %v, want KindInvalidInput", err)
	}
}

func TestWikiReachesLiveHandle(t *testing.T) {
	f := newLaunchFix(t)
	f.installGitHub(t)
	ctx := context.Background()
	_, err := NewGitHubWikiCallerForHost(ctx)
	requireAddRefusal(t, err, "wiki sync")
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	caller, err := NewGitHubWikiCallerForHost(ctx)
	if err != nil || caller == nil {
		t.Fatalf("NewGitHubWikiCallerForHost after a witnessed add = %v, %v", caller, err)
	}
	_, err = caller.Call(ctx, unknownMethod, []byte(`{}`))
	requireLiveReply(t, f, err)
}
