// Purpose: extra behavior tests for the rpcindex generator: the warning
// writer adapter, the success exit path of run, and the findings branch of
// render/generate/matchesCommitted against a throwaway git tree.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
)

func TestWarnWriterReportsFullLength(t *testing.T) {
	ww := warnWriter{output.NewDefault(false, false, false, false)}
	msg := []byte("rpcindex: something odd\n")
	n, err := ww.Write(msg)
	if err != nil {
		t.Fatalf("Write returned error %v", err)
	}
	if n != len(msg) {
		t.Fatalf("Write = %d, want %d", n, len(msg))
	}
	if n, err = ww.Write(nil); n != 0 || err != nil {
		t.Fatalf("Write(nil) = %d, %v; want 0, nil", n, err)
	}
}

func findingsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	golden := filepath.Join(root, "cmd/cascade/testdata/daemon_rpc_methods.golden")
	if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(golden, []byte("# header\nghost.method\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable: %v %s", args, err, out)
		}
	}
	return root
}

func TestRenderListsEveryFinding(t *testing.T) {
	root := findingsRoot(t)
	b, err := render(root)
	if err == nil {
		t.Fatal("render must fail for an undeclared, undocumented method")
	}
	if b != nil {
		t.Fatalf("render returned %d bytes alongside an error", len(b))
	}
	for _, want := range []string{"findings; page not written", "undeclared access: ghost.method", "undocumented method: ghost.method"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestGenerateAndMatchWriteNothingOnFindings(t *testing.T) {
	root := findingsRoot(t)
	dest := filepath.Join(root, pagePath)
	if err := generate(root, dest); err == nil || !strings.Contains(err.Error(), "ghost.method") {
		t.Fatalf("generate error = %v, want one naming ghost.method", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("generate wrote a page despite findings")
	}
	if ok, err := matchesCommitted(root); ok || err == nil {
		t.Fatalf("matchesCommitted = %v, %v; want false with error", ok, err)
	}
}

func TestGenerateReportsUnwritableDestination(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(testRoot(t), filepath.Join(blocker, "sub", "page.md")); err == nil {
		t.Fatal("generate must fail when the destination parent is a file")
	}
}
