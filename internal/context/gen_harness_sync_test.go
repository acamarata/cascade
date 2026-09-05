package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end tests for the multi-harness pipeline. They drive the real
// production entry point over a real directory tree, so discovery, merge,
// render and write are proven connected for every registered harness rather
// than merely present.

// syncFixture seeds a two-tier tree and returns its home and repo paths.
func syncFixture(t *testing.T) (home, repo string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	repo = filepath.Join(home, "sites", "project", "repo")
	seedTier(t, home, "## Style\n\nShort sentences.\n")
	seedTier(t, repo, "## Style\n\nLong sentences.\n\n## Tests\n\nRun them.\n")
	return home, repo
}

// TestGenerateHarnessInstructionsWritesEveryHarness requires the pipeline
// to materialize each registered harness's file at the place that harness
// actually reads, for both tiers that contributed.
func TestGenerateHarnessInstructionsWritesEveryHarness(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }

	if _, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	want := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, filepath.FromSlash(cxTarget.globalName)),
		filepath.Join(home, filepath.FromSlash(ocTarget.globalName)),
		filepath.Join(repo, ".claude", "CLAUDE.md"),
		filepath.Join(repo, agentsFileName),
	}
	for _, path := range want {
		got, err := os.ReadFile(path) //nolint:gosec // path is composed from this test's temp tree.
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(got), markerOpenPrefix) {
			t.Errorf("%s carries no managed block", path)
		}
	}
}

// TestGenerateHarnessInstructionsAppliesThePrecedenceRule proves the merge
// reaches the written files: the lower tier's losing section is absent from
// every harness's file, and the section no higher tier claimed survives.
func TestGenerateHarnessInstructionsAppliesThePrecedenceRule(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }
	if _, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	for _, name := range []string{filepath.Join(".claude", "CLAUDE.md"), agentsFileName} {
		path := filepath.Join(repo, name)
		got, err := os.ReadFile(path) //nolint:gosec // path is composed from this test's temp tree.
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if strings.Contains(string(got), "Long sentences.") {
			t.Errorf("%s carries a section the higher tier won", path)
		}
		if !strings.Contains(string(got), "Run them.") {
			t.Errorf("%s lost the section no higher tier defined", path)
		}
	}
}

// TestGenerateHarnessInstructionsIsIdempotent requires a repeat run to
// change nothing, for every harness at once.
func TestGenerateHarnessInstructionsIsIdempotent(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }
	first, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	again, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(again) != len(first) {
		t.Fatalf("second run considered %d files, first considered %d", len(again), len(first))
	}
	for _, r := range again {
		if r.Action != ActionUnchanged {
			t.Errorf("%s: action = %v on a repeat run, want ActionUnchanged", r.Path, r.Action)
		}
	}
}

// TestGenerateHarnessInstructionsWritesSharedPathsOnce pins the dedup rule:
// two harnesses reading the same file at the same path produce one result,
// not two, so a caller is never told two files changed when one did.
func TestGenerateHarnessInstructionsWritesSharedPathsOnce(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }
	results, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	seen := map[string]int{}
	for _, r := range results {
		seen[r.Path]++
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("%s reported %d times, want 1", path, n)
		}
	}
	if got := len(results); got != len(seen) {
		t.Fatalf("%d results cover %d distinct paths", got, len(seen))
	}
}

// TestGenerateHarnessInstructionsStopsAtAHandEdit requires the pipeline to
// surface the write path's refusal rather than swallowing it, and to hand
// back the results it did complete.
func TestGenerateHarnessInstructionsStopsAtAHandEdit(t *testing.T) {
	home, repo := syncFixture(t)
	homeFn := func() (string, error) { return home, nil }
	if _, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited); err != nil {
		t.Fatalf("first run: %v", err)
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	got, err := os.ReadFile(path) //nolint:gosec // path is composed from this test's temp tree.
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	edited := strings.Replace(string(got), "Short sentences.", "My own sentence.", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("planting the hand edit: %v", err)
	}
	if _, err := GenerateHarnessInstructions(context.Background(), repo, homeFn, RefuseIfEdited); err == nil {
		t.Fatal("the pipeline overwrote a hand-edited block")
	} else {
		assertKindConflict(t, err)
	}
	after, rerr := os.ReadFile(path) //nolint:gosec // path is composed from this test's temp tree.
	if rerr != nil {
		t.Fatalf("reading back: %v", rerr)
	}
	if string(after) != edited {
		t.Fatal("the hand edit did not survive the refusal")
	}
}

// TestGenerateHarnessInstructionsEmptyTree covers the legitimate quiet
// case: no instruction file anywhere above the working directory writes
// nothing and reports nothing wrong.
func TestGenerateHarnessInstructionsEmptyTree(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(home, "sites", "project", "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	results, err := GenerateHarnessInstructions(
		context.Background(), cwd, func() (string, error) { return home, nil }, RefuseIfEdited)
	if err != nil {
		t.Fatalf("pipeline over an empty tree: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("an empty tree produced %d writes, want 0", len(results))
	}
}
