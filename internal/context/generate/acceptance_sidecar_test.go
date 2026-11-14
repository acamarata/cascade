package generate

// Purpose: prove a full generation leaves a clean git root and that a
//   refusal over a hand-edited file writes only the sidecar.
// Constraints: repositories are real git repositories under t.TempDir();
//   the workflows probe is the recorded replay; claude is never executed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generatedIgnoreRows are the rows the repository .gitignore carries for
// generated files outside the AI workspace directories.
const generatedIgnoreRows = "/AGENTS.md\n/opencode.json\n/.codex/\n*.cascade-new\n"

// commitAll commits everything staged in root with a fixed identity.
func commitAll(t *testing.T, root string) {
	t.Helper()
	if out, err := git(t, root, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	args := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"}
	if out, err := git(t, root, args...); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// layoutRepo is acceptanceRepo with this repository's .gitignore plus the
// generated rows, committed.
func layoutRepo(t *testing.T) string {
	t.Helper()
	root := acceptanceRepo(t)
	gi, err := os.ReadFile(filepath.Join("..", "..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, ".gitignore", string(gi)+"\n"+generatedIgnoreRows)
	commitAll(t, root)
	return root
}

func TestGenerateLeavesCleanRootFullSet(t *testing.T) {
	root := layoutRepo(t)
	rec := &recorder{}
	res := acceptanceRun(t, root, fixedNow, rec)
	if len(res.Written) != len(wantManagedSet()) {
		t.Fatalf("Written = %v, want the full managed set", res.Written)
	}
	for _, p := range append(wantManagedSet(), ManifestRel) {
		if missing(root, p) {
			t.Fatalf("generated file %s is absent", p)
		}
	}
	status, err := git(t, root, "status", "--porcelain", "--ignored=no", "--untracked-files=all")
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, status)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("git status lists generated paths:\n%s", status)
	}
	if out, err := git(t, root, "check-ignore", "-q", "AGENTS.md"); err != nil {
		t.Fatalf("control: AGENTS.md is not ignored: %v\n%s", err, out)
	}
}

func TestGenerateRefusalSidecar(t *testing.T) {
	root := layoutRepo(t)
	acceptanceRun(t, root, fixedNow, &recorder{})
	const target = ".claude/rules/root.md"
	edited := slurp(t, root, target) + "\nHand note: run the slow suite first.\n"
	put(t, root, target, edited)
	put(t, root, "go.mod", "module example.com/acceptance\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n")
	put(t, root, "pkg/extra.py", "print('x')\n")
	rec := &recorder{}
	res := acceptanceRun(t, root, fixedNow, rec)
	if got := slurp(t, root, target); got != edited {
		t.Fatalf("hand-edited file changed:\n%s", got)
	}
	if len(res.Refused) != 1 || res.Refused[0].Path != target || res.Refused[0].Reason != ReasonOutsideEdited {
		t.Fatalf("Refused = %+v, want one outside-edited refusal of %s", res.Refused, target)
	}
	if missing(root, target+".cascade-new") {
		t.Fatal("the sidecar was not written")
	}
	want := sinkCall{target, target + ".cascade-new", ReasonOutsideEdited}
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Fatalf("attention calls = %+v, want exactly %+v", rec.calls, want)
	}
}
