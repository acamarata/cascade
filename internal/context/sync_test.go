package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDriftCheckReportsMissingAsStale requires a tree that has never been
// synced to report every file stale with a reason that names the file as
// missing, not merely "different" — a fresh install must never read as
// healthy (see sync.go's DriftResult doc comment).
func TestDriftCheckReportsMissingAsStale(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	result, err := Sync(context.Background(), repo, homeFn, true)
	if err != nil {
		t.Fatalf("Sync(checkOnly=true): %v", err)
	}
	if len(result.Drift) == 0 {
		t.Fatal("Drift is empty on a never-synced tree, want one entry per generated file")
	}
	for _, d := range result.Drift {
		if !d.Stale {
			t.Errorf("%s/%s: Stale = false on a never-synced tree", d.Harness, d.Path)
		}
		if d.Reason != "missing on disk" {
			t.Errorf("%s/%s: Reason = %q, want %q", d.Harness, d.Path, d.Reason, "missing on disk")
		}
	}
}

// TestSyncRegeneratesThenReportsFresh proves the two commands agree: after
// a regenerate run, --check must report every file fresh.
func TestSyncRegeneratesThenReportsFresh(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	first, err := Sync(context.Background(), repo, homeFn, false)
	if err != nil {
		t.Fatalf("Sync(checkOnly=false): %v", err)
	}
	if first.Regenerated == 0 {
		t.Fatal("Regenerated = 0 on a never-synced tree, want at least one file written")
	}
	if first.AlreadyFresh != 0 {
		t.Errorf("AlreadyFresh = %d on the first run, want 0", first.AlreadyFresh)
	}

	check, err := Sync(context.Background(), repo, homeFn, true)
	if err != nil {
		t.Fatalf("Sync(checkOnly=true) after regenerate: %v", err)
	}
	for _, d := range check.Drift {
		if d.Stale {
			t.Errorf("%s/%s: Stale = true right after regenerating, reason=%q", d.Harness, d.Path, d.Reason)
		}
	}
}

// TestSyncIsIdempotent is the forge §5.9 convergence contract: a second
// regenerate run over an already-synced tree writes nothing.
func TestSyncIsIdempotent(t *testing.T) {
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
		t.Errorf("Regenerated = %d on the second run, want 0", second.Regenerated)
	}
	if second.AlreadyFresh == 0 {
		t.Error("AlreadyFresh = 0 on the second run, want every file already-fresh")
	}
	for _, r := range second.Files {
		if r.Action != ActionUnchanged {
			t.Errorf("%s: Action = %v on the second run, want ActionUnchanged", r.Path, r.Action)
		}
	}
}

// TestDriftCheckDetectsHandEdit requires a hand-edited managed block to
// report stale even though the file exists: an edit away from the fresh
// generation is exactly the drift this check exists to surface.
func TestDriftCheckDetectsHandEdit(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }
	if _, err := Sync(context.Background(), repo, homeFn, false); err != nil {
		t.Fatalf("Sync(checkOnly=false): %v", err)
	}

	target := filepath.Join(repo, ".claude", "CLAUDE.md")
	original, err := os.ReadFile(target) //nolint:gosec // fixed test path.
	if err != nil {
		t.Fatalf("reading %s: %v", target, err)
	}
	edited := strings.Replace(string(original), "Run them.", "Run them differently.", 1)
	if edited == string(original) {
		t.Fatal("fixture has no 'Run them.' body to edit; test setup is stale")
	}
	if err := os.WriteFile(target, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing hand edit: %v", err)
	}

	check, err := Sync(context.Background(), repo, homeFn, true)
	if err != nil {
		t.Fatalf("Sync(checkOnly=true) after hand edit: %v", err)
	}
	found := false
	for _, d := range check.Drift {
		if d.Path != target {
			continue
		}
		found = true
		if !d.Stale {
			t.Errorf("%s: Stale = false after a hand edit", target)
		}
		if !strings.Contains(d.Reason, "hand-edited") {
			t.Errorf("%s: Reason = %q, want it to name the hand edit", target, d.Reason)
		}
	}
	if !found {
		t.Fatalf("no Drift entry for %s", target)
	}
}

// TestDriftCheckPropagatesGeneratorError requires a MergedContext that
// could not have come from MergeTiers (sections but no provenance map) to
// return the generator's own typed error, not a swallowed one and not a
// panic, while still recording which harness failed.
func TestDriftCheckPropagatesGeneratorError(t *testing.T) {
	mc := MergedContext{
		Sections: []MergedSection{{Heading: "X", Content: "## X\n\nY", Role: TierGCI, Ordinal: 0}},
		// Provenance intentionally nil: validateMergedContext's documented
		// signal that mc did not come from MergeTiers.
	}
	results, err := DriftCheck(context.Background(), mc, map[TierRole]string{TierGCI: "/tmp"})
	if err == nil {
		t.Fatal("DriftCheck(malformed MergedContext) = nil error, want a typed KindInvalidInput error")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("KindOf(err) = %v, want KindInvalidInput", err)
	}
	if len(results) != 1 || !results[0].Stale || results[0].Reason == "" {
		t.Errorf("results = %+v, want exactly one Stale entry with a non-empty Reason", results)
	}
}

// TestDriftCheckNilAndEmptyMergedContextNoPanic is the Article-1 floor: a
// zero-value MergedContext and a nil roots map must never panic, and must
// report a clean empty check rather than an error — an empty input is a
// legitimate "nothing to render" state, not a failure (HarnessGenerator's
// own contract).
func TestDriftCheckNilAndEmptyMergedContextNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DriftCheck panicked on a zero-value MergedContext: %v", r)
		}
	}()
	results, err := DriftCheck(context.Background(), MergedContext{}, nil)
	if err != nil {
		t.Fatalf("DriftCheck(zero MergedContext) = %v, want nil error", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %+v, want empty", results)
	}
}

// TestSyncCheckOnlyWritesNothing requires --check to be strictly read-only,
// even on a tree that has never been synced (every file reported stale).
func TestSyncCheckOnlyWritesNothing(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	if _, err := Sync(context.Background(), repo, homeFn, true); err != nil {
		t.Fatalf("Sync(checkOnly=true): %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("stat(%s) err = %v, want IsNotExist (a --check run must write nothing)", "CLAUDE.md", err)
	}
}
