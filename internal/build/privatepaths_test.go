// Tests for privatepaths.go (P1-SHIP-14 / AUD-041).
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// privatepathsModuleRoot locates the repo root by walking up from this
// file, same pattern as cleanrootModuleRoot/sweepModuleRoot/
// commitsModuleRoot.
func privatepathsModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("private-paths gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("private-paths gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// checkNoPrivateTrackedCase is one table row for
// TestCheckNoPrivateTracked_Unit.
type checkNoPrivateTrackedCase struct {
	name    string
	tracked []string
	want    []string
}

// privCase builds a row whose single tracked path must be reported (report=true)
// or allowed (report=false), keeping each table row on one line.
func privCase(name, path string, report bool) checkNoPrivateTrackedCase {
	if report {
		return checkNoPrivateTrackedCase{name, []string{path}, []string{path}}
	}
	return checkNoPrivateTrackedCase{name, []string{path}, nil}
}

// checkNoPrivateTrackedCases is the table for TestCheckNoPrivateTracked_Unit,
// split across helpers to stay under the ≤50-line func gate.
func checkNoPrivateTrackedCases() []checkNoPrivateTrackedCase {
	out := append(checkNoPrivateTrackedBaseCases(), checkNoPrivateTrackedNestedCases()...)
	return append(out, checkNoPrivateTrackedTestdataAndCaseCases()...)
}

// checkNoPrivateTrackedBaseCases covers root-level matching (clean tree,
// each private dir, lookalikes, empty/root-file entries).
func checkNoPrivateTrackedBaseCases() []checkNoPrivateTrackedCase {
	return []checkNoPrivateTrackedCase{
		{"clean tree", []string{"README.md", "go.mod", "internal/build/sweep.go"}, nil},
		{"claude prefix", []string{"README.md", ".claude/hygiene/identifier-patterns.txt"},
			[]string{".claude/hygiene/identifier-patterns.txt"}},
		{"opencode and cascade prefixes", []string{".opencode/state.json", ".cascade/scratch.md", "go.mod"},
			[]string{".opencode/state.json", ".cascade/scratch.md"}},
		// Segments that merely START WITH the same letters are not the
		// private directory itself and must never be flagged.
		{"lookalike prefixes are not flagged", []string{".clauded/notes.txt", ".cascader/x.go"}, nil},
		{"empty entries and root file named .claude are ignored safely", []string{"", "docs/.claude-note.md"}, nil},
	}
}

// checkNoPrivateTrackedNestedCases covers nested private dirs at depth,
// the one fixture allow, and near-misses.
func checkNoPrivateTrackedNestedCases() []checkNoPrivateTrackedCase {
	return []checkNoPrivateTrackedCase{
		privCase("nested claude dir is caught (apps/ is an allowed root dir)", "apps/ui/.claude/settings.local.json", true),
		privCase("nested opencode dir is caught", "a/b/.opencode/y", true),
		privCase("nested cascade dir is caught", "c/.cascade/z", true),
		// The one tracked-on-purpose fixture must stay allowed.
		privCase("the migration fixture is allowed", "internal/migration/testdata/fixture_project/.claude/CLAUDE.md", false),
		privCase("near-miss .claudex", ".claudex/notes.txt", false),
		privCase("near-miss x.claude", "x.claude/notes.txt", false),
		privCase("near-miss .claude-foo", ".claude-foo/n", false),
		privCase("nested near-miss .Claudex", "apps/.Claudex/n", false),
	}
}

// checkNoPrivateTrackedTestdataAndCaseCases covers the second confirm
// review: a testdata segment anywhere never exempts a private path (only
// the exact fixture prefix does), and private segment names match
// case-insensitively (git on a case-insensitive filesystem honours
// `.claude/` in .gitignore for `.Claude`, so a force-add into it is the
// same bypass).
func checkNoPrivateTrackedTestdataAndCaseCases() []checkNoPrivateTrackedCase {
	return []checkNoPrivateTrackedCase{
		privCase("testdata below a root private dir", ".claude/testdata/x", true),
		privCase("testdata below a nested private dir", "apps/ui/.claude/testdata/x", true),
		privCase("private dir below a root testdata", "testdata/.claude/top", true),
		privCase("private dir below a nested testdata", "apps/testdata/.claude/settings.local.json", true),
		privCase("opencode below a nested testdata", "pkg/testdata/.opencode/q", true),
		privCase("another migration testdata dir is not the fixture", "internal/migration/testdata/other/.claude/x", true),
		privCase("fixture prefix must match from the root", "x/internal/migration/testdata/fixture_project/.claude/y", true),
		privCase("root .Claude", ".Claude/x", true),
		privCase("root .CASCADE", ".CASCADE/x", true),
		privCase("root .OpenCode", ".OpenCode/x", true),
		privCase("nested .Claude", "apps/.Claude/x", true),
		privCase("nested .CASCADE", "apps/.CASCADE/z", true),
		privCase("nested .OpenCode", "a/b/.OpenCode/y", true),
	}
}

func TestCheckNoPrivateTracked_Unit(t *testing.T) {
	for _, tc := range checkNoPrivateTrackedCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckNoPrivateTracked(tc.tracked)
			if len(got) != len(tc.want) {
				t.Fatalf("CheckNoPrivateTracked(%v) = %v, want %v", tc.tracked, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("CheckNoPrivateTracked(%v) = %v, want %v", tc.tracked, got, tc.want)
				}
			}
		})
	}
}

// TestNoPrivatePathsTracked_Live proves the real checkout carries zero
// tracked paths under .claude/, .opencode/ or .cascade/ — the acceptance
// gate itself, run as part of the ordinary `go test ./internal/build/...`
// so it blocks the build/CI/ship path on any future force-added private
// file.
func TestNoPrivatePathsTracked_Live(t *testing.T) {
	root := privatepathsModuleRoot(t)
	files, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("private-paths gate: ListTrackedFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("private-paths gate: git ls-files returned zero tracked paths — gate regression, not a clean repo")
	}
	violations := CheckNoPrivateTracked(files)
	if len(violations) != 0 {
		t.Fatalf("private-paths gate: %d private path(s) tracked in the real repo: %v", len(violations), violations)
	}
}

// seededForcePaths are the private paths TestNoPrivatePathsTracked_SeededForceAdd
// force-adds. ignored=true rows are also asserted gitignored first, so the
// test proves the -f BYPASS; the case variant is not asserted ignored
// because that depends on core.ignorecase (true on case-insensitive
// filesystems, where `.claude/` in .gitignore also hides `.Claude`).
var seededForcePaths = []struct {
	path    string
	ignored bool
}{
	{".claude/hygiene/identifier-patterns.txt", true}, // sweep.go's pattern-file path
	{"apps/ui/.claude/settings.local.json", true},     // nested under an allowed root
	{"apps/ui/.claude/testdata/leak", true},           // testdata below a private dir
	{"apps/.Claude/x", false},                         // case variant, distinct dir
}

// TestNoPrivatePathsTracked_SeededForceAdd proves the gate fires on the
// exact bypass the rationale names: `git add -f` overrides .gitignore, so
// the enforceable form is a gate over `git ls-files`, never the ignore
// rule itself. A fresh temp repo is seeded with a clean baseline commit,
// then private-tree files are force-added (left staged — `git ls-files`
// reports the index either way), and CheckNoPrivateTracked must report
// every one of them, by the exact path git recorded.
func TestNoPrivatePathsTracked_SeededForceAdd(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "gate@example.invalid")
	runGit(t, dir, "config", "user.name", "gate")
	writeFileT(t, filepath.Join(dir, "README.md"), "seeded baseline\n")
	// Mirror the real repo's ignore rule for the private-workspace trees.
	writeFileT(t, filepath.Join(dir, ".gitignore"), ".claude/\n.opencode/\n.cascade/\n")
	runGit(t, dir, "add", "README.md", ".gitignore")
	runGit(t, dir, "commit", "-q", "-m", "chore: seed baseline")

	addArgs := []string{"add", "-f"}
	for _, sp := range seededForcePaths {
		writeFileT(t, filepath.Join(dir, filepath.FromSlash(sp.path)), "SEEDED-NOT-A-REAL-PATTERN\n")
		if sp.ignored {
			// check-ignore exits 0 when the path IS ignored.
			ignoreCmd := exec.Command("git", "check-ignore", "-q", sp.path)
			ignoreCmd.Dir = dir
			if err := ignoreCmd.Run(); err != nil {
				t.Fatalf("private-paths gate: expected %s to be gitignored before the force-add: %v", sp.path, err)
			}
		}
		addArgs = append(addArgs, sp.path)
	}
	runGit(t, dir, addArgs...)

	files, err := ListTrackedFiles(dir)
	if err != nil {
		t.Fatalf("private-paths gate: ListTrackedFiles: %v", err)
	}
	reported := map[string]bool{}
	for _, v := range CheckNoPrivateTracked(files) {
		reported[v] = true
	}
	for _, sp := range seededForcePaths {
		if !reported[sp.path] {
			t.Errorf("private-paths gate: expected %s to be reported after `git add -f`, got reported=%v (tracked=%v)", sp.path, reported, files)
		}
	}
}
