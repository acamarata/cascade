package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

// buildFixtureRepo materializes files as a fresh committed git repo with
// no baseline file at all (so InitDocTruthBaseline can create one).
func buildFixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dst := t.TempDir()
	for rel, content := range files {
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dst}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	run("add", "-A")
	run("commit", "-q", "-m", "chore: seed doctruth gen fixture")
	return dst
}

func doctruthGenFixtureFiles() map[string]string {
	return map[string]string{
		"README.md":                 "# Doc\n\nTODO fix\n",
		".github/wiki/Home.md":      "# Home\n",
		"docs/security-posture.md":  "# Security Posture\n",
		"docs/quickstart/README.md": "# Quickstart\n",
	}
}

// TestDocTruthBaselineGenDeterministic proves acceptance [6]: two baseline
// --init runs over byte-identical trees produce byte-identical, sorted,
// LF-terminated output that holds no absolute path.
func TestDocTruthBaselineGenDeterministic(t *testing.T) {
	repoA := buildFixtureRepo(t, doctruthGenFixtureFiles())
	repoB := buildFixtureRepo(t, doctruthGenFixtureFiles())

	if _, err := build.InitDocTruthBaseline(repoA); err != nil {
		t.Fatalf("InitDocTruthBaseline(A): %v", err)
	}
	if _, err := build.InitDocTruthBaseline(repoB); err != nil {
		t.Fatalf("InitDocTruthBaseline(B): %v", err)
	}

	dataA, err := os.ReadFile(filepath.Join(repoA, "internal/build/testdata/doctruth-baseline.json"))
	if err != nil {
		t.Fatalf("reading A: %v", err)
	}
	dataB, err := os.ReadFile(filepath.Join(repoB, "internal/build/testdata/doctruth-baseline.json"))
	if err != nil {
		t.Fatalf("reading B: %v", err)
	}
	if string(dataA) != string(dataB) {
		t.Fatalf("two --init runs over identical trees diverged:\nA=%s\nB=%s", dataA, dataB)
	}
	if len(dataA) == 0 || dataA[len(dataA)-1] != '\n' {
		t.Fatal("baseline file must end with a newline")
	}
	if strings.Contains(string(dataA), repoA) {
		t.Fatal("baseline holds an absolute path")
	}
}

// runIn invokes run in-process and returns its exit code and streams.
func runIn(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, output.New(&out, &errb, false, false, false, false), root)
	return code, out.String(), errb.String()
}

const (
	todoLine     = "README.md:3: claim: stale marker: TODO"
	baselineFile = "internal/build/testdata/doctruth-baseline.json"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRun_CheckBaselineLifecycle(t *testing.T) {
	repo := buildFixtureRepo(t, doctruthGenFixtureFiles())

	code, out, errs := runIn(t, repo, "check")
	if code != 2 || out != "" || !strings.HasPrefix(errs, "error: not-found: doctruth: reading baseline:") {
		t.Fatalf("check without baseline = %d %q %q", code, out, errs)
	}

	code, out, errs = runIn(t, repo, "baseline", "--init")
	if code != 0 || out != "baseline initialized: 1 findings\n" || errs != "" {
		t.Fatalf("baseline --init = %d %q %q", code, out, errs)
	}

	summary := "checked 4 files, 0 links, 0 refs, 0 directives, 0 findings\n"
	code, out, errs = runIn(t, repo, "check")
	if code != 0 || out != todoLine+" (baselined)\n"+summary || errs != "" {
		t.Fatalf("check baselined = %d %q %q", code, out, errs)
	}

	code, out, _ = runIn(t, repo, "check", "--release")
	wantRel := todoLine + "\nchecked 4 files, 0 links, 0 refs, 0 directives, 1 findings\n"
	if code != 1 || out != wantRel {
		t.Fatalf("check --release = %d %q, want 1 %q", code, out, wantRel)
	}

	code, out, errs = runIn(t, repo, "baseline")
	if code != 0 || out != "baseline pruned: 1 kept, 0 dropped\n" || errs != "" {
		t.Fatalf("baseline prune = %d %q %q", code, out, errs)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Doc\n\nclean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runIn(t, repo, "baseline")
	if code != 0 || out != "baseline pruned: 0 kept, 1 dropped\n" {
		t.Fatalf("baseline prune after fix = %d %q", code, out)
	}

	code, out, errs = runIn(t, repo, "baseline", "--init")
	if code != 2 || out != "" || !strings.HasPrefix(errs, "error: conflict: doctruth: baseline already exists at "+baselineFile) {
		t.Fatalf("second --init = %d %q %q", code, out, errs)
	}
}

func TestRun_CheckNewFindingsExitsOne(t *testing.T) {
	files := doctruthGenFixtureFiles()
	files["README.md"] = "# Doc\n\nclean\n"
	repo := buildFixtureRepo(t, files)
	if code, _, errs := runIn(t, repo, "baseline", "--init"); code != 0 {
		t.Fatalf("init = %d %q", code, errs)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Doc\n\nTODO fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runIn(t, repo, "check")
	want := todoLine + "\nchecked 4 files, 0 links, 0 refs, 0 directives, 1 findings\n"
	if code != 1 || out != want || errs != "" {
		t.Fatalf("check = %d %q %q, want 1 %q", code, out, errs, want)
	}
}

func TestRun_Guard(t *testing.T) {
	files := doctruthGenFixtureFiles()
	files["README.md"] = "# Doc\n\nclean\n"
	repo := buildFixtureRepo(t, files)
	if code, _, errs := runIn(t, repo, "baseline", "--init"); code != 0 {
		t.Fatalf("init = %d %q", code, errs)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "chore: commit baseline")

	code, out, errs := runIn(t, repo, "guard", "HEAD")
	if code != 0 || out != "guard: no keys added\n" || errs != "" {
		t.Fatalf("guard clean = %d %q %q", code, out, errs)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Doc\n\nTODO fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, baselineFile)); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runIn(t, repo, "baseline", "--init"); code != 0 {
		t.Fatalf("re-init = %d %q", code, errs)
	}
	code, out, errs = runIn(t, repo, "guard", "HEAD")
	if code != 1 || !strings.HasPrefix(out, "added: ") || strings.Count(out, "\n") != 1 || errs != "" {
		t.Fatalf("guard added = %d %q %q", code, out, errs)
	}

	code, out, errs = runIn(t, repo, "guard", "no-such-ref")
	if code != 2 || out != "" || !strings.HasPrefix(errs, "error: unavailable: doctruth: git show no-such-ref:") {
		t.Fatalf("guard bad ref = %d %q %q", code, out, errs)
	}
}

func TestRun_UsageErrors(t *testing.T) {
	repo := buildFixtureRepo(t, doctruthGenFixtureFiles())
	cases := []struct {
		args []string
		want string
	}{
		{nil, "error: usage: doctruth <check|baseline|guard> [args...]\n"},
		{[]string{"bogus"}, "error: doctruth: unknown subcommand \"bogus\"\n"},
		{[]string{"baseline", "x"}, "error: doctruth: baseline takes no arguments except --init\n"},
		{[]string{"guard"}, "error: doctruth: guard requires exactly one git ref\n"},
		{[]string{"guard", "a", "b"}, "error: doctruth: guard requires exactly one git ref\n"},
	}
	for _, c := range cases {
		code, out, errs := runIn(t, repo, c.args...)
		if code != 2 || out != "" || errs != c.want {
			t.Errorf("%v = %d %q %q, want 2 %q", c.args, code, out, errs, c.want)
		}
	}
}

func TestRepoRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	wantR, _ := filepath.EvalSymlinks(root)
	gotR, _ := filepath.EvalSymlinks(got)
	if gotR != wantR {
		t.Fatalf("repoRoot = %s, want %s", gotR, wantR)
	}
}

func TestRun_FindingsSortedByFileThenLine(t *testing.T) {
	files := doctruthGenFixtureFiles()
	files["README.md"] = "# Doc\n\nTODO b\n\nTODO a\n"
	files["docs/security-posture.md"] = "# Security Posture\n\nTODO c\n"
	repo := buildFixtureRepo(t, files)
	if code, _, errs := runIn(t, repo, "baseline", "--init"); code != 0 {
		t.Fatalf("init = %d %q", code, errs)
	}
	code, out, _ := runIn(t, repo, "check", "--release")
	want := "README.md:3: claim: stale marker: TODO\n" +
		"README.md:5: claim: stale marker: TODO\n" +
		"docs/security-posture.md:3: claim: stale marker: TODO\n" +
		"checked 4 files, 0 links, 0 refs, 0 directives, 3 findings\n"
	if code != 1 || out != want {
		t.Fatalf("check --release = %d %q, want 1 %q", code, out, want)
	}
}

func TestRun_PruneWithoutBaselineFails(t *testing.T) {
	repo := buildFixtureRepo(t, doctruthGenFixtureFiles())
	code, out, errs := runIn(t, repo, "baseline")
	if code != 2 || out != "" || !strings.HasPrefix(errs, "error: ") {
		t.Fatalf("prune without baseline = %d %q %q", code, out, errs)
	}
}

func TestRepoRoot_NoGoMod(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := repoRoot(); err == nil || !strings.Contains(err.Error(), "no go.mod found above") {
		t.Fatalf("repoRoot err = %v", err)
	}
}
