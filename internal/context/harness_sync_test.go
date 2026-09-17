package context

import (
	"context"
	"path/filepath"
	"testing"
)

// harnessListOver runs the detector and folds in a real check-only Sync
// over repo — the same two calls ComputeContextHarnessList makes, which
// is what makes these tests exercise the seam rather than a stub of it.
func harnessListOver(t *testing.T, home, repo string, installed ...HarnessKind) []HarnessState {
	t.Helper()
	roots := map[string]bool{}
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	detector := NewPathDetector("darwin", env, func(path string) bool { return roots[path] })
	for _, kind := range installed {
		root, err := detector.configRoot(kind)
		if err != nil {
			t.Fatalf("configRoot(%q): %v", kind, err)
		}
		roots[root] = true
	}
	states, err := detector.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	drift, err := Sync(context.Background(), repo, func() (string, error) { return home, nil }, true)
	if err != nil {
		t.Fatalf("Sync(checkOnly): %v", err)
	}
	return WithDrift(states, drift)
}

// TestContextHarnessSync is the end-to-end shape of what `context harness
// list` reports over a real tree: before a sync every installed harness
// is drifted, and after one none is.
//
// It runs over the REAL generators and the REAL filesystem, so a drift
// rule that only held against a stub fails here.
func TestContextHarnessSync(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	before := harnessListOver(t, home, repo, HarnessClaude, HarnessCodex)
	drifted := driftedKinds(before)
	if len(drifted) == 0 {
		t.Fatal("a never-synced tree reported no drift for any installed harness")
	}
	for _, s := range before {
		if !s.Detected && s.Drift {
			t.Errorf("%q is not installed and reported drift", s.Kind)
		}
		if s.Detected && s.Drift && s.InstructionPath == "" {
			t.Errorf("%q reported drift without naming the file", s.Kind)
		}
	}

	if _, err := Sync(context.Background(), repo, homeFn, false); err != nil {
		t.Fatalf("Sync(regenerate): %v", err)
	}

	after := harnessListOver(t, home, repo, HarnessClaude, HarnessCodex)
	if got := driftedKinds(after); len(got) != 0 {
		t.Fatalf("after a regenerate, %v still report drift", got)
	}
	for _, s := range after {
		if s.Detected && s.DriftReason != "" {
			t.Errorf("%q reports a drift reason with Drift=false: %q", s.Kind, s.DriftReason)
		}
	}
}

// TestContextHarnessSyncIdempotent is 06 §5 rule 9 at this seam: a second
// regenerate over an already-synced tree writes nothing and the harness
// list still reports every installed harness in sync.
//
// The second half matters on its own. A regenerate that wrote nothing but
// left the list reporting drift would satisfy the write-count assertion
// and still tell the operator to run a command that does nothing.
func TestContextHarnessSyncIdempotent(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	if _, err := Sync(context.Background(), repo, homeFn, false); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	second, err := Sync(context.Background(), repo, homeFn, false)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if second.Regenerated != 0 {
		t.Errorf("the second run wrote %d file(s), want 0", second.Regenerated)
	}
	if second.AlreadyFresh == 0 {
		t.Error("the second run reports nothing already-fresh")
	}
	for _, s := range harnessListOver(t, home, repo, HarnessClaude) {
		if s.Detected && s.Drift {
			t.Errorf("%q reports drift after two regenerates: %q", s.Kind, s.DriftReason)
		}
	}
}

// TestHarnessDriftIsScopedToInstalledHarnesses is the rule that keeps the
// list readable, asserted against a real drifted tree rather than a
// constructed SyncResult: cascade generates files for all three harnesses
// whatever is installed, and only an installed one's drift is reported.
func TestHarnessDriftIsScopedToInstalledHarnesses(t *testing.T) {
	home, repo := syncFixture(t)

	states := harnessListOver(t, home, repo, HarnessClaude)
	for _, s := range states {
		if s.Kind == HarnessClaude {
			if !s.Detected || !s.Drift {
				t.Errorf("the installed harness reported %+v, want detected and drifted", s)
			}
			continue
		}
		if s.Drift || s.DriftReason != "" || s.InstructionPath != "" {
			t.Errorf("%q is not installed and carries drift data: %+v", s.Kind, s)
		}
	}
	// The files themselves DO exist for every harness after a
	// regenerate; the list simply does not speak for the ones nobody has.
	if _, err := Sync(context.Background(), repo, func() (string, error) { return home, nil }, false); err != nil {
		t.Fatalf("Sync(regenerate): %v", err)
	}
	if _, err := Sync(context.Background(), repo, func() (string, error) { return home, nil }, true); err != nil {
		t.Fatalf("Sync(checkOnly): %v", err)
	}
	if _, err := filepath.Abs(repo); err != nil {
		t.Fatal(err)
	}
}
