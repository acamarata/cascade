package main

// Purpose: `cascade context slice` and `cascade context show`'s own tests
//   (E/S-09.T2): the reachability proof (mounted on the real root command
//   tree, matching R-14.166's discipline) and the Article-4 behavioral
//   coverage floor, driving the real cobra commands end to end against a
//   temp-dir embedded cascade.db — no real socket, no "net"/"net/http"
//   import (Art.7.2's default unit lane). Reuses execRootContextScope and
//   fakeContextScopePaths from context_scope_test.go (same package) rather
//   than declaring a second, identical harness.
// SPORT: cmd.cascade.cmd.context-slice-show (ADD, per T-2 sport_updates).

import (
	"strings"
	"testing"
)

// TestContextSliceShowAreMountedOnRoot is the R-14.166 reachability proof
// for both new subcommands: removing registerContextEngineHandlers from
// buildRPCServer would still leave these CLI verbs mounted (they fall back
// to the embedded runtime), so this test alone does not prove RPC wiring —
// see this ticket's journal for the daemon-side mutation-proof result
// (comment out RegisterContextAssembleHandler's call, watch
// TestContextAssembleHandlerRegistersBothMethods go red, restore it).
func TestContextSliceShowAreMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{{"context", "slice"}, {"context", "show"}} {
		found, _, err := root.Find(path)
		if err != nil || found.Name() != path[len(path)-1] {
			t.Fatalf("%v is not mounted on the root command: found=%v err=%v", path, safeName(found), err)
		}
	}
}

// TestContextSliceCLIBehavioralCoverage drives `context slice` through its
// human and --json output forms, its --budget override, its --budget 0
// invalid-input error path, and its unknown-subcommand/positional-arg error
// paths — the Article-4 CLI behavioral floor.
func TestContextSliceCLIBehavioralCoverage(t *testing.T) {
	dir := t.TempDir()

	t.Run("human", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "context", "slice")
		if err != nil {
			t.Fatalf("cascade context slice: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, "SLOT") || !strings.Contains(got, "tier") || !strings.Contains(got, "total") {
			t.Errorf("human output missing a slot line:\n%s", got)
		}
	})

	t.Run("json", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "--json", "context", "slice")
		if err != nil {
			t.Fatalf("cascade --json context slice: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, `"ok": true`) || !strings.Contains(got, `"counts"`) {
			t.Errorf("--json output missing the envelope/counts shape:\n%s", got)
		}
	})

	t.Run("budget override", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "context", "slice", "--budget", "500")
		if err != nil {
			t.Fatalf("cascade context slice --budget 500: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, "SLOT") {
			t.Errorf("human output missing a slot line:\n%s", got)
		}
	})

	t.Run("budget zero is invalid input", func(t *testing.T) {
		_, err := execRootContextScope(t, dir, "context", "slice", "--budget", "0")
		if err == nil {
			t.Fatal("cascade context slice --budget 0 = nil error, want invalid-input")
		}
	})

	t.Run("rejects positional args", func(t *testing.T) {
		_, err := execRootContextScope(t, dir, "context", "slice", "extra-arg")
		if err == nil {
			t.Fatal("cascade context slice extra-arg = nil error, want invalid-input")
		}
	})
}

// TestContextShowCLIBehavioralCoverage drives `context show` through its
// human and --json output forms (an empty-tier result is a legitimate
// zero-slot, exit-0 outcome — the isolated temp dir this test runs
// against resolves no tiers), and its positional-arg error path.
func TestContextShowCLIBehavioralCoverage(t *testing.T) {
	dir := t.TempDir()

	t.Run("human", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "context", "show")
		if err != nil {
			t.Fatalf("cascade context show: %v\noutput:\n%s", err, got)
		}
		if got == "" {
			t.Error("human output is empty")
		}
	})

	t.Run("json", func(t *testing.T) {
		got, err := execRootContextScope(t, dir, "--json", "context", "show")
		if err != nil {
			t.Fatalf("cascade --json context show: %v\noutput:\n%s", err, got)
		}
		if !strings.Contains(got, `"tiers"`) {
			t.Errorf("--json output missing tiers array:\n%s", got)
		}
	})

	t.Run("rejects positional args", func(t *testing.T) {
		_, err := execRootContextScope(t, dir, "context", "show", "extra-arg")
		if err == nil {
			t.Fatal("cascade context show extra-arg = nil error, want invalid-input")
		}
	})
}
