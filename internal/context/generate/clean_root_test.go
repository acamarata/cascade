package generate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir with a hermetic environment and returns stdout.
func git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGenerateLeavesCleanRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	repo, _ := newRepo(t)
	if out, err := git(t, repo, "init", "-q"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// The repository's own .gitignore is the fixture: it ignores .cascade/.
	gi, err := os.ReadFile(filepath.Join("..", "..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, repo, ".gitignore", string(gi))
	if out, err := git(t, repo, "add", ".gitignore"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	rec := &recorder{}
	w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
	for _, p := range []string{"AGENTS.md", ".cascade/policy-kernel.md"} {
		out, err := w.Apply(context.Background(), Rendered{Path: p, GeneratorID: genA, Content: []byte(baseFile())})
		if err != nil || !out.Written {
			t.Fatalf("Apply %s = %+v %v", p, out, err)
		}
	}
	if err := WriteManifest(repo, w.Manifest(fixedNow, nil)); err != nil {
		t.Fatal(err)
	}
	if missing(repo, ManifestRel) || missing(repo, ".cascade/policy-kernel.md") {
		t.Fatal("the .cascade files were not generated")
	}
	if out, err := git(t, repo, "ls-files", ".cascade"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("git ls-files .cascade = %q %v", out, err)
	}
	status, err := git(t, repo, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, status)
	}
	if strings.Contains(status, ".cascade") {
		t.Fatalf("git status lists a .cascade path:\n%s", status)
	}
	if !strings.Contains(status, "AGENTS.md") {
		t.Fatalf("control: the generated AGENTS.md should show as untracked:\n%s", status)
	}
	if out, err := git(t, repo, "check-ignore", "-q", ManifestRel); err != nil {
		t.Fatalf("manifest is not ignored: %v\n%s", err, out)
	}
}
