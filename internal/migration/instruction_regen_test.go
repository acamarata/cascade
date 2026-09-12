// Purpose: P1-E26-W10-S53-T3's own tests. TestDriftReport is the "can this
//
//	detector actually report drift" proof this ticket's brief demands: it
//	runs the real scan+diff pipeline against a deliberately stale
//	checked-in fixture (RED — drift must be reported) and then against
//	the exact fresh content the real generator produces for the same
//	input (GREEN — no drift), so a detector that always says "fresh"
//	cannot pass this file.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).
package migration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// styleRecords builds the single-tier []TierRecord this whole file's
// fixture uses: one PRI-tier CASCADE.md whose body is "## Style\n\n<style>.\n".
// Hand-built (not read via Discover) so every test here is hermetic: it
// never depends on this repo's own real tier files or git root.
func styleRecords(dir, style string) []cascadecontext.TierRecord {
	return []cascadecontext.TierRecord{{
		Role: cascadecontext.TierPRI, Ordinal: 0, Dir: dir,
		Path:    filepath.Join(dir, ".cascade", "CASCADE.md"),
		Content: "## Style\n\n" + style + "\n",
	}}
}

// wantFreshCLAUDEmd is the exact, deterministic byte content
// CCInstructionWriter{}.Generate produces for styleRecords(dir, "Short
// sentences.") — captured once from the real generator (Article-2: a real
// counterpart, not a hand-typed guess) and pinned here because the
// managed-block digest is a content hash with no clock or randomness
// (Art.7.3), so it never changes for this fixed input.
const wantFreshCLAUDEmd = "<!-- cascade:generate-instructions digest=sha256:2685b1cce720933cc0da48bd66c91972504a6f863dbd05f3d64e59e5cde35eb0 -->\n" +
	"## Cascade Context — PRI Tier (Per-Repo Instructions)\n\n" +
	"**MCP server:** `stdio: cascade mcp stdio`\n\n" +
	"Call `cascade.search` before responding to queries about this project.\n" +
	"Call `cascade.context_slice` to retrieve relevant context from the RAG index.\n" +
	"If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.\n\n" +
	"## Style\n\nShort sentences.\n\n" +
	"<!-- /cascade:generate-instructions -->\n"

// TestDriftReportRed feeds the detector a known-drifted input — the
// checked-in testdata/fixture_project/.claude/CLAUDE.md, which was written
// deliberately stale ("Long sentences...") — and asserts it reports drift.
// A detector that can never report drift (always "fresh") fails this test.
func TestDriftReportRed(t *testing.T) {
	dir := "testdata/fixture_project"
	mc, err := cascadecontext.MergeTiers(styleRecords(dir, "Short sentences."))
	if err != nil {
		t.Fatalf("MergeTiers: %v", err)
	}
	roots := map[cascadecontext.TierRole]string{cascadecontext.TierPRI: dir}
	entries, err := driftEntriesForProject(mc, roots)
	if err != nil {
		t.Fatalf("driftEntriesForProject: %v", err)
	}
	// Two files drift: the checked-in, deliberately stale CLAUDE.md (claude
	// harness) and the never-yet-written AGENTS.md (codex/opencode harness,
	// deduped to one entry since both target the same path).
	if len(entries) != 2 {
		t.Fatalf("RED case: want exactly 2 drifted files, got %d: %+v", len(entries), entries)
	}
	got := entries[0]
	wantPath := filepath.Join(dir, ".claude", "CLAUDE.md")
	if got.HarnessFile != wantPath {
		t.Fatalf("HarnessFile = %q, want %q", got.HarnessFile, wantPath)
	}
	if got.Added == 0 || got.Removed == 0 {
		t.Fatalf("RED case: want both Added>0 and Removed>0 (the style line changed), got Added=%d Removed=%d", got.Added, got.Removed)
	}
	assertGoldenReport(t, "testdata/golden_drift_report.json", Report{
		Projects: []ProjectReport{{ProjectPath: dir, Drift: entries}},
	})
}

// TestDriftReportGreen feeds the SAME detector the exact fresh content the
// real generator produces for the same tier input and asserts it reports
// NO drift — the other half of the RED/GREEN proof: a detector that always
// says "stale" would fail this half.
func TestDriftReportGreen(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"), []byte(wantFreshCLAUDEmd), 0o600); err != nil {
		t.Fatalf("write fresh fixture: %v", err)
	}
	// AGENTS.md (codex/opencode) renders byte-identical content to
	// CLAUDE.md for this single-PRI-tier fixture; write the same bytes so
	// this half of the proof is genuinely "nothing drifts", not "one file
	// still drifts and the assertion below happens not to check it".
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(wantFreshCLAUDEmd), 0o600); err != nil {
		t.Fatalf("write fresh AGENTS.md fixture: %v", err)
	}
	mc, err := cascadecontext.MergeTiers(styleRecords(dir, "Short sentences."))
	if err != nil {
		t.Fatalf("MergeTiers: %v", err)
	}
	roots := map[cascadecontext.TierRole]string{cascadecontext.TierPRI: dir}
	entries, err := driftEntriesForProject(mc, roots)
	if err != nil {
		t.Fatalf("driftEntriesForProject: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("GREEN case: want zero drift against fresh content, got %+v", entries)
	}
}

// assertGoldenReport compares got's JSON encoding against the frozen fixture
// at path byte-for-byte (CI reads it read-only; a real regen of this fixture
// is a manual, reviewed step, never something a test does to itself).
func assertGoldenReport(t *testing.T, path string, got Report) {
	t.Helper()
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(gotJSON)+"\n" != string(want) {
		t.Fatalf("golden report mismatch.\n--- got ---\n%s\n--- want (%s) ---\n%s", gotJSON, path, want)
	}
}

// TestLoadProjectList covers the happy path plus both documented error
// conditions: a missing file and a malformed one (a directory).
func TestLoadProjectList(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "projects.txt")
	content := "# comment\n\n" + filepath.Join(dir, "a") + "\nrelative-b\n"
	if err := os.WriteFile(listPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write list: %v", err)
	}
	got, err := LoadProjectList(listPath, dir)
	if err != nil {
		t.Fatalf("LoadProjectList: %v", err)
	}
	want := []string{filepath.Join(dir, "a"), filepath.Join(dir, "relative-b")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("LoadProjectList = %v, want %v", got, want)
	}

	if _, err := LoadProjectList(filepath.Join(dir, "missing.txt"), dir); err == nil {
		t.Fatal("missing project list: want an error, got nil")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("missing project list: kind=%v ok=%v, want KindNotFound", kind, ok)
	}

	if _, err := LoadProjectList(dir, dir); err == nil {
		t.Fatal("malformed (directory) project list: want an error, got nil")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("malformed project list: kind=%v ok=%v, want KindInvalidInput", kind, ok)
	}

	empty, err := LoadProjectList("", dir)
	if err != nil || empty != nil {
		t.Fatalf("empty path: want (nil, nil), got (%v, %v)", empty, err)
	}
}

// TestMergeProjectPaths asserts dedup, order, and list-before-registered
// precedence.
func TestMergeProjectPaths(t *testing.T) {
	got := MergeProjectPaths([]string{"/a", "/b", "/a"}, []string{"/b", "/c"})
	want := []string{"/a", "/b", "/c"}
	if len(got) != len(want) {
		t.Fatalf("MergeProjectPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MergeProjectPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// realProject builds a hermetic, git-free project directory under a fresh
// t.TempDir(): a `.cascade/CASCADE.md` tier source (so Discover's real,
// unmodified pipeline has genuine content to merge and generate from) and
// an isolated HOME so no ambient tier file on the machine running this
// test — or this very repo's own real GCI/ASI/PPI files — can leak into
// the merge. gitRoot falls back to the project directory itself (no `.git`
// anywhere under a fresh temp dir), matching TestDiscoverGitBinaryAbsent's
// documented behavior.
func realProject(t *testing.T, style string) (dir string, homeDir cascadecontext.HomeDirFunc) {
	t.Helper()
	home := t.TempDir()
	dir = filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(dir, ".cascade"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cascade", "CASCADE.md"), []byte("## Style\n\n"+style+"\n"), 0o600); err != nil {
		t.Fatalf("write tier file: %v", err)
	}
	return dir, func() (string, error) { return home, nil }
}

// TestInstructionRegenUnreadableProjectContinues drives Run over one
// nonexistent project and one real, drifted one (via the real
// Discover/MergeTiers/Generate pipeline, never a hand-built MergedContext):
// the report must be partial, must carry the first project's error, and
// must still carry the second project's real drift — a scan that stops at
// the first failure would fail this test.
func TestInstructionRegenUnreadableProjectContinues(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	realDir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{missing, realDir},
		HomeDir:      homeDir,
		CheckOnly:    true,
	})
	if err == nil {
		t.Fatal("want a non-nil partial-failure error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("kind=%v ok=%v, want KindNotFound (propagated from the missing project)", kind, ok)
	}
	if !report.Partial {
		t.Fatal("want report.Partial=true")
	}
	if len(report.Projects) != 2 {
		t.Fatalf("want 2 project entries, got %d", len(report.Projects))
	}
	if report.Projects[0].Error == "" {
		t.Fatal("want the missing project's entry to carry an error")
	}
	if len(report.Projects[1].Drift) == 0 {
		t.Fatal("want the second, real project's drift to still be reported despite the first project's failure")
	}
}
