//go:build !windows

package claude

// Purpose: the canary scan of every file the job created or modified (T0
//   decision F3): exact, hex and base64 content, a tracked file the job
//   changed, a nested untracked directory, a symlink target, a form split
//   across two reads, and a FIFO that must fail closed without blocking.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   artifacts.go. Every leak is written by the fake harness into its own
//   leased tree; the clean control proves the scan passes a benign file.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestClaudeCanaryInWrittenFileFailsClosed(t *testing.T) {
	leaks := map[string]string{
		"exact content":             `printf 'x=%s\n' "$C" > out.txt`,
		"hex content":               `printf '%s' "$C" | od -An -tx1 | tr -d ' \n' > out.hex`,
		"base64 content":            `printf 'ab%s' "$C" | base64 > out.b64`,
		"a tracked file it changed": `printf '%s' "$C" >> tracked.txt`,
		"a nested untracked dir":    `mkdir -p sub/deep && printf '%s' "$C" > sub/deep/x`,
		"a symlink target":          `ln -s "/nowhere/$C" link`,
		"upper-case content":        `printf '%s' "$C" | tr a-z A-Z > upper.txt`,
		"a git-ignored directory":   `mkdir -p ign && printf '%s' "$C" > ign/x`,
		"split across two reads":    `head -c 65530 /dev/zero | tr '\0' a > big.txt && printf '%s' "$C" >> big.txt`,
	}
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			mustWrite(t, filepath.Join(f.root, "tracked.txt"), "base\n")
			mustWrite(t, filepath.Join(f.root, ".gitignore"), "ign/\n")
			gitIn(t, f.root, "add", "tracked.txt", ".gitignore")
			gitIn(t, f.root, "commit", "--quiet", "-m", "base")
			f.body = readCanary + `[ -n "$C" ] || exit 9` + "\n" + leak + "\n" + f.body
			_, cr, err := run(t, f.driver(f.config()), f.spec())
			if err != ErrCredentialCanary || cr.Output != "" || strings.Contains(err.Error(), "cascade-canary-") {
				t.Fatalf("Collect = %v (output %q): a canary in a written file must fail the run", err, cr.Output)
			}
		})
	}
	t.Run("a benign file passes", func(t *testing.T) {
		f := newFx(t)
		f.body = readCanary + `[ -n "$C" ] || exit 9` + "\nprintf 'plain\\n' > made.txt\n" + f.body
		_, cr, err := run(t, f.driver(f.config()), f.spec())
		if err != nil || !reflect.DeepEqual(cr.Artifacts, []string{"made.txt"}) {
			t.Fatalf("Collect = %v, artifacts %q", err, cr.Artifacts)
		}
	})
}

// TestScanArtifactFIFOFailsClosed: git status does not list a FIFO, but a
// path can be swapped for one; neither the Lstat branch nor the open blocks.
func TestScanArtifactFIFOFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	forms := canaryForms("cascade-canary-x")
	if err := scanArtifact(t.Context(), root, "pipe", forms); err == nil ||
		!strings.Contains(err.Error(), "could not be scanned for the canary") {
		t.Fatalf("scanArtifact(fifo) = %v", err)
	}
	if err := scanRegular(t.Context(), filepath.Join(root, "pipe"), "pipe", forms); err == nil {
		t.Fatal("scanRegular read a FIFO")
	}
}

func TestScanArtifactDeletedPathHasNothingToRead(t *testing.T) {
	if err := scanArtifact(t.Context(), t.TempDir(), "gone.txt", canaryForms("cascade-canary-x")); err != nil {
		t.Fatalf("a deleted path: %v", err)
	}
	if err := scanArtifact(t.Context(), "\x00", "x", nil); err == nil {
		t.Fatal("an unreadable path must fail closed")
	}
}
