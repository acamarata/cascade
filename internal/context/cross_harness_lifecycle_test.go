package context

// Purpose: the lifecycle half of the cross-harness suite
//   (P1-E16-W4-S35-T4): install convergence and drift triggering, over
//   the real write pipeline rather than over a generator in isolation.
//   Split from cross_harness_test.go for Art.10.3's 300-line cap.
// SPORT: internal/context cross-harness conformance (ADD) — P1-E16-W4-S35-T4.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCrossHarnessInstallIdempotency is 06 §5.9's convergence contract
// across all three at once: a second run over an already-synced tree
// writes nothing and reports every file unchanged.
func TestCrossHarnessInstallIdempotency(t *testing.T) {
	project, _, _ := crossHarnessFixture(t)
	home := filepath.Dir(project) // unused root; Sync resolves home via the func below
	_ = home
	homeFn := func() (string, error) { return t.TempDir(), nil }

	first, err := GenerateHarnessInstructions(context.Background(), project, homeFn, RefuseIfEdited)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first run considered no files")
	}
	second, err := GenerateHarnessInstructions(context.Background(), project, homeFn, RefuseIfEdited)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, r := range second {
		if r.Action != ActionUnchanged {
			t.Errorf("%s: Action = %d on the second run, want ActionUnchanged", r.Path, r.Action)
		}
	}
	if len(second) != len(first) {
		t.Errorf("second run considered %d files, first considered %d", len(second), len(first))
	}
}

// TestCrossHarnessDriftTrigger requires a mutated managed block to report
// stale for EVERY harness that reads the mutated file — which is the whole
// point of AlsoServes, stated as behaviour rather than as a field.
func TestCrossHarnessDriftTrigger(t *testing.T) {
	project, _, _ := crossHarnessFixture(t)
	homeDir := t.TempDir()
	writeTier(t, filepath.Join(homeDir, ".cascade"), "# Global\n\nShort sentences.\n")
	homeFn := func() (string, error) { return homeDir, nil }

	if _, err := GenerateHarnessInstructions(context.Background(), project, homeFn, RefuseIfEdited); err != nil {
		t.Fatalf("seeding a synced tree: %v", err)
	}
	shared := filepath.Join(project, "AGENTS.md")
	original, err := os.ReadFile(shared) //nolint:gosec // fixed test path.
	if err != nil {
		t.Fatalf("the shared instruction file was not written: %v", err)
	}
	mutated := strings.Replace(string(original), "Run the tests.", "Run nothing.", 1)
	if mutated == string(original) {
		t.Fatal("fixture body changed; this test no longer mutates anything")
	}
	if err := os.WriteFile(shared, []byte(mutated), 0o600); err != nil {
		t.Fatalf("writing the mutation: %v", err)
	}

	result, err := Sync(context.Background(), project, homeFn, true)
	if err != nil {
		t.Fatalf("Sync(checkOnly): %v", err)
	}
	var entry *DriftResult
	for i := range result.Drift {
		if result.Drift[i].Path == shared {
			entry = &result.Drift[i]
		}
	}
	if entry == nil {
		t.Fatalf("no drift entry for the mutated %s", shared)
	}
	if !entry.Stale {
		t.Error("a hand-edited managed block did not report stale")
	}
	if len(entry.AlsoServes) == 0 {
		t.Error("the shared file names one harness; the other reads the same mutated bytes and is told it is in sync")
	}
}
