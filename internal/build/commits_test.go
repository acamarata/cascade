// Tests for commits.go (P1-E01-W1-S01-T5). Table-driven unit tests exercise
// the pure parser and merge-skip logic; TestConventionalCommitGate_Live is
// the opt-in live gate the pre-push hook and CI hygiene job invoke — see
// commits.go's package doc for why it no-ops without CASCADE_HYGIENE_RUN=1.
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runGit runs a real git subcommand in dir and fails the test on error.
// Shared by commits_test.go, sweep_test.go and hook_test.go (all exercise
// real git repos per Art.2 — never a hand-authored stand-in for git's
// behavior).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// writeFileT writes data to path, creating parent directories as needed,
// and fails the test on error.
func writeFileT(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeFileT: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("writeFileT: write %s: %v", path, err)
	}
}

// commitsModuleRoot locates the repo root by walking up from this file.
func commitsModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("commits gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("commits gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

func TestValidateCommitMessage(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantErr bool
	}{
		{"simple feat", "feat: add thing", false},
		{"scoped fix", "fix(build): correct thing", false},
		{"breaking bang", "feat(runtime)!: change the wire format", false},
		{"multiline body and trailer", "chore(deps): bump toml\n\nBody line.\n\nSigned-off-by: A <a@example.invalid>", false},
		{"unknown type", "oops: not a real type", true},
		{"no colon", "feat add thing without colon", true},
		{"empty subject", "feat: ", true},
		{"empty message", "", true},
		{"uppercase type rejected", "Feat: add thing", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateCommitMessage(c.msg)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateCommitMessage(%q) error = %v, wantErr %v", c.msg, err, c.wantErr)
			}
		})
	}
}

func TestValidateCommitRange_SkipsMergeCommits(t *testing.T) {
	commits := []CommitRef{
		{SHA: "a", Message: "feat: fine", Merge: false},
		{SHA: "b", Message: "Merge branch 'x' into y (not conventional at all)", Merge: true},
		{SHA: "c", Message: "definitely not conventional", Merge: false},
	}
	v := ValidateCommitRange(commits)
	if len(v) != 1 || v[0].SHA != "c" {
		t.Fatalf("expected exactly one violation for SHA c (merge skipped), got %+v", v)
	}
}

func TestRunConventionalCommitGate(t *testing.T) {
	if err := RunConventionalCommitGate([]CommitRef{{SHA: "a", Message: "feat: fine"}}); err != nil {
		t.Fatalf("expected nil error for a clean range, got %v", err)
	}
	err := RunConventionalCommitGate([]CommitRef{
		{SHA: "a", Message: "feat: fine"},
		{SHA: "b", Message: "not conventional"},
	})
	if err == nil {
		t.Fatal("expected an aggregated error for a range containing a violation")
	}
	if !strings.Contains(err.Error(), "1 violation") {
		t.Fatalf("expected aggregated error to report the violation count, got: %v", err)
	}
}

func TestValidateCommitMessage_SeededFixtures(t *testing.T) {
	root := commitsModuleRoot(t)
	bad, err := os.ReadFile(filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "commits", "bad-message.txt"))
	if err != nil {
		t.Fatalf("reading bad-message fixture: %v", err)
	}
	if err := ValidateCommitMessage(strings.TrimRight(string(bad), "\n")); err == nil {
		t.Fatal("expected the seeded bad-message fixture to fail validation, got nil")
	}

	good, err := os.ReadFile(filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "commits", "good-message.txt"))
	if err != nil {
		t.Fatalf("reading good-message fixture: %v", err)
	}
	if err := ValidateCommitMessage(string(good)); err != nil {
		t.Fatalf("expected the seeded good-message fixture to pass validation, got %v", err)
	}
}

func TestParseGitLogOutput(t *testing.T) {
	// Synthetic bytes in exactly the --pretty format LoadCommitRange
	// requests: hash, parents (space-separated, empty for a root commit,
	// two for a merge), body (may itself contain newlines).
	raw := "aaa1\x1f\x1ffeat: first\x1e" +
		"\nbbb2\x1faaa1\x1ffix: second\n\nBody line.\n\nSigned-off-by: A <a@example.invalid>\x1e" +
		"\nccc3\x1faaa1 bbb2\x1fMerge branch 'x' into y\x1e"
	refs := parseGitLogOutput([]byte(raw))
	if len(refs) != 3 {
		t.Fatalf("expected 3 parsed commits, got %d: %+v", len(refs), refs)
	}
	if refs[0].SHA != "aaa1" || refs[0].Merge {
		t.Fatalf("commit 0: expected root commit aaa1, non-merge, got %+v", refs[0])
	}
	if refs[1].SHA != "bbb2" || refs[1].Merge || !strings.Contains(refs[1].Message, "Signed-off-by") {
		t.Fatalf("commit 1: expected non-merge with trailer intact, got %+v", refs[1])
	}
	if refs[2].SHA != "ccc3" || !refs[2].Merge {
		t.Fatalf("commit 2: expected a two-parent merge commit, got %+v", refs[2])
	}
}

func TestLoadCommitRange_RealGit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "fixture@example.invalid")
	runGit(t, dir, "config", "user.name", "Fixture")
	writeFileT(t, filepath.Join(dir, "a.txt"), "one\n")
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "feat: first commit")
	writeFileT(t, filepath.Join(dir, "a.txt"), "two\n")
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "not conventional at all")

	refs, err := LoadCommitRange(dir, "HEAD")
	if err != nil {
		t.Fatalf("LoadCommitRange: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 real commits over HEAD, got %d: %+v", len(refs), refs)
	}
	v := ValidateCommitRange(refs)
	if len(v) != 1 {
		t.Fatalf("expected exactly 1 violation from the real 2-commit range, got %d: %+v", len(v), v)
	}
}

// FuzzCommitMessage (06 §5.7) proves ValidateCommitMessage never panics
// and is deterministic (same input, same verdict) across arbitrary byte
// input, including invalid UTF-8, empty strings, and pathological header
// shapes a hand-written table can't anticipate. Seed corpus: testdata's
// default fuzz location for this package is
// internal/build/testdata/fuzz/FuzzCommitMessage/ (Go's corpus-location
// convention: <package>/testdata/fuzz/<FuzzName>/), seeded with a handful
// of representative headers below.
func FuzzCommitMessage(f *testing.F) {
	for _, seed := range []string{
		"feat: add thing",
		"fix(build)!: correct thing",
		"chore(deps): bump toml\n\nBody.\n\nSigned-off-by: A <a@example.invalid>",
		"not conventional at all",
		"",
		"feat: ",
		"Merge branch 'x' into y",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		err1 := ValidateCommitMessage(msg)
		err2 := ValidateCommitMessage(msg)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("ValidateCommitMessage(%q) not deterministic: %v vs %v", msg, err1, err2)
		}
	})
}

// TestConventionalCommitGate_Live runs the gate against a real range only
// when explicitly requested (see commits.go package doc); otherwise it is
// a documented no-op so a bare `go test ./...` from any other concurrent
// ticket's session never trips it.
func TestConventionalCommitGate_Live(t *testing.T) {
	if os.Getenv("CASCADE_HYGIENE_RUN") != "1" {
		t.Skip("CASCADE_HYGIENE_RUN not set; live conventional-commit gate not requested")
	}
	rng := os.Getenv("CASCADE_HYGIENE_COMMIT_RANGE")
	if rng == "" {
		t.Fatal("conventional-commit gate: CASCADE_HYGIENE_RUN=1 but CASCADE_HYGIENE_COMMIT_RANGE is empty (fail closed)")
	}
	root := commitsModuleRoot(t)
	commits, err := LoadCommitRange(root, rng)
	if err != nil {
		t.Fatalf("conventional-commit gate: loading range %q: %v", rng, err)
	}
	if err := RunConventionalCommitGate(commits); err != nil {
		t.Fatal(err)
	}
}
