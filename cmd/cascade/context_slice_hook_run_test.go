package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/context/hydration"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/sqlite"
)

// hookCommandFor builds a cobra command wired to stdin/stdout buffers and
// the given context, so a test can drive runContextSliceHook exactly as
// the flag's RunE does.
func hookCommandFor(ctx context.Context, t *testing.T, stdin []byte) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetIn(bytes.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd, &out
}

// hookTestDeps points the hook at a temp CASCADE_HOME.
func hookTestDeps(t *testing.T) contextScopeDeps {
	t.Helper()
	return contextScopeDeps{
		Paths:   doctorTestPaths(t),
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
}

// TestContextSliceHookTimeoutFailOpen is the fail-open rule: hydration is
// context, not policy, so a hook that cannot produce a capsule in time
// prints NOTHING and exits 0 rather than blocking the user's prompt.
//
// The context is cancelled before the hook runs, which is the cheapest
// way to reach the same branch a real timeout reaches — the slice call
// fails, and what matters is what the hook does next.
func TestContextSliceHookTimeoutFailOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd, out := hookCommandFor(ctx, t, realCCPayload(t))

	if err := runContextSliceHook(cmd, hookTestDeps(t)); err != nil {
		t.Fatalf("the hook returned an error and would have failed the prompt: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("the hook printed something after failing: %q", out.String())
	}
}

// TestContextSliceHookNeverFailsAPrompt walks every payload that leaves
// the hook with nothing to say. All of them must be silent and exit 0:
// there is no input to this hook worth interrupting someone's typing for.
func TestContextSliceHookNeverFailsAPrompt(t *testing.T) {
	deps := hookTestDeps(t)
	for name, stdin := range map[string]string{
		"empty stdin":       "",
		"not json":          "{{{",
		"no cwd":            `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`,
		"a different event": `{"cwd":"/tmp","hook_event_name":"PreToolUse"}`,
	} {
		t.Run(name, func(t *testing.T) {
			cmd, out := hookCommandFor(context.Background(), t, []byte(stdin))
			if err := runContextSliceHook(cmd, deps); err != nil {
				t.Fatalf("returned an error: %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("printed %q", out.String())
			}
		})
	}
}

// TestHydrationDegradedEventPublish proves the degraded signal is a real
// event on the real bus, readable by the same count the doctor check
// uses. Without it the fail-open design would be genuinely invisible: the
// user sees a session with no context and no way to tell why.
func TestHydrationDegradedEventPublish(t *testing.T) {
	paths := doctorTestPaths(t)
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := contextScopeDeps{Paths: paths, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}

	ctx := context.Background()
	recordHydrationDegraded(ctx, deps, "slice_failed")
	recordHydrationDegraded(ctx, deps, "payload_undecodable")

	// The hook closes the store on its way out; if it did not, this open
	// would fail on the exclusive lock — which is the regression this
	// line quietly guards.
	store, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("open the store the hook wrote to (a leaked lock looks exactly like this): %v", err)
	}
	defer func() { _ = store.Close() }()

	count, err := hydration.CountDegraded(ctx, store, time.Now(), hydration.DegradedWindow)
	if err != nil {
		t.Fatalf("CountDegraded: %v", err)
	}
	if count != 2 {
		t.Fatalf("counted %d degraded events, want 2", count)
	}
}

// TestHydrationDegradedPublishNeverPanics pins the best-effort contract:
// the hook reaches this call on paths where nothing is working, and a
// panic there would turn a silent degradation into a crashed prompt.
func TestHydrationDegradedPublishNeverPanics(*testing.T) {
	recordHydrationDegraded(context.Background(), contextScopeDeps{}, "no paths at all")
	hydration.PublishDegraded(context.Background(), nil, []byte(`{}`))
}

// TestHydrationSettingsFallBackToTheDefaults asserts the hook's own
// posture, which is the OPPOSITE of the loader's on purpose: a config it
// cannot read yields the documented defaults rather than a refusal,
// because one prompt losing its context to a typo in an unrelated table
// is a worse outcome than running with the values the docs describe.
func TestHydrationSettingsFallBackToTheDefaults(t *testing.T) {
	got := hydrationSettings(contextScopeDeps{
		Paths:   doctorTestPaths(t),
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
	want := hydration.Default()
	if got != want {
		t.Fatalf("settings = %+v, want the defaults %+v", got, want)
	}
}

// TestTheHydrationCheckIsMountedInProduction is the Art.10.5 assertion:
// the doctor check is registered by the real composition root, not merely
// built. A check nobody mounts reports nothing, and a report missing a
// check looks exactly like a healthy one.
func TestTheHydrationCheckIsMountedInProduction(t *testing.T) {
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), cascaderuntime.SystemClock{})
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	if _, mounted := reg.Lookup(hydration.CheckName); !mounted {
		t.Fatalf("%q is not mounted by the production doctor registry", hydration.CheckName)
	}
}
