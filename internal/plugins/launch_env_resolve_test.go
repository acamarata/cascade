//go:build !windows

// Purpose: a launch environment is usable only whole (refusals, no default
// fallback) and CloseLaunched stops every live handle.
// SPORT: internal/plugins trusted launch tests (P1-PLG-03).
package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestResolveLaunchEnvRefusals: a launch environment is usable only whole.
// No source, a failing source, and a source missing the store, the effect
// writer or the environment reader each refuse, and nothing falls back to a
// default; a source that omits the stderr sink gets a discarding one.
func TestResolveLaunchEnvRefusals(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	full, _ := f.source(ctx)
	if full.Store == nil || full.Effects == nil || full.Getenv == nil {
		t.Fatal("the fixture's own environment is incomplete; the cases below prove nothing")
	}
	sourceErr := cascade.New(cascade.KindTimeout, "the source itself failed")
	for _, tc := range []struct {
		name string
		src  LaunchEnvSource
		kind cascade.Kind
		want string
	}{
		{"no source", nil, cascade.KindUnavailable, "no plugin launch environment is wired"},
		{"source error", func(context.Context) (LaunchEnv, error) { return full, sourceErr }, cascade.KindTimeout, "the source itself failed"},
		{"no store", func(context.Context) (LaunchEnv, error) { e := full; e.Store = nil; return e, nil }, cascade.KindUnavailable, "no plugin store"},
		{"no effect writer", func(context.Context) (LaunchEnv, error) { e := full; e.Effects = nil; return e, nil }, cascade.KindUnavailable, "no effect writer"},
		{"no environment reader", func(context.Context) (LaunchEnv, error) { e := full; e.Getenv = nil; return e, nil }, cascade.KindUnavailable, "no environment reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetLaunchEnvSource(tc.src)()
			env, err := ResolveLaunchEnv(ctx)
			if !cascade.HasKind(err, tc.kind) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ResolveLaunchEnv = %v; want kind %v naming %q", err, tc.kind, tc.want)
			}
			if env.Store != nil || env.Effects != nil || env.Getenv != nil {
				t.Fatalf("a refused resolve returned a partly filled environment: %+v", env)
			}
		})
	}
	// A source error comes back as the source reported it, not re-wrapped.
	restore := SetLaunchEnvSource(func(context.Context) (LaunchEnv, error) { return full, sourceErr })
	if _, err := ResolveLaunchEnv(ctx); err != error(sourceErr) {
		t.Fatalf("ResolveLaunchEnv rewrote the source's error: %v", err)
	}
	restore()

	restore = SetLaunchEnvSource(func(context.Context) (LaunchEnv, error) { e := full; e.Stderr = nil; return e, nil })
	defer restore()
	env, err := ResolveLaunchEnv(ctx)
	if err != nil || env.Stderr == nil {
		t.Fatalf("ResolveLaunchEnv without a stderr sink = %+v, %v; want a usable sink", env, err)
	}
	if n, werr := env.Stderr.Write([]byte("x")); n != 1 || werr != nil {
		t.Fatalf("the default stderr sink = %d, %v; want it to swallow writes", n, werr)
	}
	if env.Store != full.Store || env.Getenv("HOME") != f.env["HOME"] {
		t.Fatal("ResolveLaunchEnv did not hand back the source's own store and reader")
	}
}

// TestSetLaunchEnvSourceRestoresThePreviousSource: a daemon source replaces
// the CLI's for as long as it is installed, and restore puts the CLI's back.
func TestSetLaunchEnvSourceRestoresThePreviousSource(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	marker := cascade.New(cascade.KindConflict, "second source")
	restore := SetLaunchEnvSource(func(context.Context) (LaunchEnv, error) { return LaunchEnv{}, marker })
	if _, err := ResolveLaunchEnv(ctx); err != error(marker) {
		t.Fatalf("the replacing source was not honoured: %v", err)
	}
	restore()
	env, err := ResolveLaunchEnv(ctx)
	if err != nil || env.Store != f.store {
		t.Fatalf("after restore ResolveLaunchEnv = %+v, %v; want the fixture's source back", env, err)
	}
}

// TestCloseLaunchedStopsEveryLiveHandle: a daemon's shutdown closes every
// launched child, still closes the rest when one close fails, and returns
// the first failure; afterwards nothing is tracked and no call goes through.
func TestCloseLaunchedStopsEveryLiveHandle(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	if err := CloseLaunched(ctx); err != nil {
		t.Fatalf("CloseLaunched with nothing live = %v", err)
	}
	a := liveGitHub(t, f)
	b, err := LaunchInstalled(ctx, cascadeGitHubPluginID)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(ctx) })
	if n := len(launchedHandles()); n != 2 {
		t.Fatalf("%d handles tracked after two launches, want 2", n)
	}
	realClose := closeProcess
	var closed int
	failure := errors.New("child refused to stop")
	closeProcess = func(ctx context.Context, h *process.Handle) error {
		closed++
		err := realClose(ctx, h) // really stop it, then report the injected failure
		if err != nil {
			t.Errorf("real close: %v", err)
		}
		return failure
	}
	defer func() { closeProcess = realClose }()
	if err := CloseLaunched(ctx); err != failure {
		t.Fatalf("CloseLaunched = %v, want the first close failure", err)
	}
	if closed != 2 {
		t.Fatalf("%d children closed, want both despite the first failure", closed)
	}
	if n := len(launchedHandles()); n != 0 {
		t.Fatalf("%d handles still tracked after CloseLaunched", n)
	}
	for name, h := range map[string]*TrustedHandle{"first": a, "second": b} {
		if _, err := h.Call(ctx, helloMethod, nil); !cascade.HasKind(err, cascade.KindUnavailable) ||
			!strings.Contains(err.Error(), "Call after Close") {
			t.Fatalf("Call on the %s handle after CloseLaunched = %v, want a refusal", name, err)
		}
	}
	if err := CloseLaunched(ctx); err != nil {
		t.Fatalf("a second CloseLaunched = %v, want nil", err)
	}
}
