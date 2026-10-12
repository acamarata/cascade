// Package repo (this file) tests the ingest admission guard over real git
// repositories under t.TempDir(). Secret-shaped fixtures are synthetic and
// assembled at run time.
//
// SPORT: repo/ingest-admission-guard/ADD test coverage.
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// guardRepo makes a real git repository holding files (rel -> content), with
// HOME and CASCADE_HOME isolated.
func guardRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, full, content, 0o644)
	}
	return dir
}

func mustWrite(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// admitOne admits rel; Admit's only error is cancellation.
func admitOne(t *testing.T, root, rel string) Admission {
	t.Helper()
	a, err := Admit(context.Background(), root, rel)
	if err != nil {
		t.Fatalf("Admit(%q): %v", rel, err)
	}
	return a
}

// wantRefused asserts rel is refused with reason and no content.
func wantRefused(t *testing.T, root, rel, reason string) Admission {
	t.Helper()
	a := admitOne(t, root, rel)
	if a.Admitted || a.Reason != reason || a.Content != nil {
		t.Errorf("%q: got admitted=%v reason=%q content=%d bytes, want refused %q", rel, a.Admitted, a.Reason, len(a.Content), reason)
	}
	return a
}

// canary is a synthetic vendor-key-shaped value.
func canary() string { return "sk" + "-ant-" + strings.Repeat("Zq7", 10) }

func TestIngestGuardExcludesDotfilesAndVendored(t *testing.T) {
	bad := []string{".env", "a/.env", "a/b/.aws/credentials", ".git/config", "node_modules/x.js", "src/vendor/y.go",
		"target/z", "dist/a.js", "pkg/build/o", "", "/etc/passwd", "../x", "a/../../x", "a\x00b", ":memo.txt", ":!x", "cert.p12::$DATA", "a:b", "cert.p12/", "cert.p12" + string(filepath.Separator), string(filepath.Separator) + "x", "a//b", "x.log ", "x.log.", "dir./f"}
	for _, rel := range bad {
		if !ExcludedPath(rel) {
			t.Errorf("ExcludedPath(%q) = false, want true", rel)
		}
	}
	for _, rel := range []string{"main.go", "src/a/b.go", "docs/vendored.md", "builder/x.go", "a..b/c"} {
		if ExcludedPath(rel) {
			t.Errorf("ExcludedPath(%q) = true, want false", rel)
		}
	}
	root := guardRepo(t, map[string]string{".env": "A=b\n", "x/node_modules/m.js": "1\n", "ok.go": "package x\n"})
	wantRefused(t, root, ".env", ReasonExcluded)
	wantRefused(t, root, "x/node_modules/m.js", ReasonExcluded)
	wantRefused(t, root, "../outside", ReasonExcluded)
	if a := admitOne(t, root, "ok.go"); !a.Admitted || a.Reason != "" || string(a.Content) != "package x\n" {
		t.Errorf("ok.go not admitted with its bytes: %+v", a)
	}
}

func TestIngestGuardRefusesKeyMaterial(t *testing.T) {
	root := guardRepo(t, map[string]string{"a.pem": "x", "b/C.KEY": "x", "d.p12": "x", "e.pfx": "x", "f.keys.txt": "plain\n"})
	for _, rel := range []string{"a.pem", "b/C.KEY", "d.p12", "e.pfx"} {
		wantRefused(t, root, rel, ReasonKeyMaterial)
	}
	if !admitOne(t, root, "f.keys.txt").Admitted {
		t.Error("f.keys.txt is not key material but was refused")
	}
}

func TestIngestGuardVCSIgnored(t *testing.T) {
	root := guardRepo(t, map[string]string{".gitignore": "gen/\n*.out\n", "gen/a.go": "package a\n", "b.out": "x\n", "c.go": "package c\n"})
	wantRefused(t, root, "gen/a.go", ReasonIgnored)
	wantRefused(t, root, "b.out", ReasonIgnored)
	if !admitOne(t, root, "c.go").Admitted {
		t.Error("c.go is not ignored but was refused")
	}
}

func TestIngestGuardRefusesSecretFile(t *testing.T) {
	root := guardRepo(t, map[string]string{"cfg.go": "package c\n\nconst k = \"" + canary() + "\"\n"})
	a := wantRefused(t, root, "cfg.go", ReasonSecret)
	if a.PatternClass != string(secrets.ClassAPIKey) {
		t.Errorf("PatternClass = %q, want %q", a.PatternClass, secrets.ClassAPIKey)
	}
	blob, _ := json.Marshal(a)
	for _, s := range []string{string(blob), fmt.Sprintf("%+v %#v", a, a)} {
		if strings.Contains(s, canary()) || strings.Contains(s, "Zq7Zq7") {
			t.Fatalf("the canary reached a returned field: %s", s)
		}
	}
}

func TestIngestGuardReadErrorRefuses(t *testing.T) {
	root := guardRepo(t, map[string]string{"real.go": "package r\n"})
	wantRefused(t, root, "missing.go", ReasonUnreadable)
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		p := filepath.Join(root, "locked.go")
		mustWrite(t, p, "package l\n", 0o000)
		wantRefused(t, root, "locked.go", ReasonUnreadable)
	}
	// A symlink is refused as excluded, never followed (tracked link to a key).
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	mustWrite(t, outside, "not a real key\n", 0o000)
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wantRefused(t, root, "link.txt", ReasonExcluded)
	// A symlinked parent directory cannot lead the read out of the tree.
	dir := filepath.Dir(outside)
	if err := os.Chmod(outside, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, root, "sub/id_ed25519", ReasonExcluded)
}

func TestIngestGuardDetectorErrorRefuses(t *testing.T) {
	root := guardRepo(t, map[string]string{"a.go": "package a\n"})
	orig := processDetector
	t.Cleanup(func() { processDetector = orig })
	processDetector = func() (*secrets.Detector, error) { return nil, errors.New("invalid [secrets] section") }
	wantRefused(t, root, "a.go", ReasonUnreadable)
	processDetector = func() (*secrets.Detector, error) { return nil, nil }
	wantRefused(t, root, "a.go", ReasonUnreadable)
	processDetector = orig
	if !admitOne(t, root, "a.go").Admitted {
		t.Error("a.go refused with a working detector")
	}
}

func TestIngestGuardGitErrorRefusesAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	for _, rel := range []string{"a.go", "b.go"} {
		mustWrite(t, filepath.Join(dir, rel), "package a\n", 0o644)
	}
	out, err := AdmitBatch(context.Background(), dir, []string{"a.go", "b.go", ".env"})
	if err != nil || len(out) != 3 {
		t.Fatalf("AdmitBatch: %v, %d results", err, len(out))
	}
	for _, a := range out {
		if a.Admitted || a.Reason != ReasonUnreadable || a.Content != nil {
			t.Errorf("%q: a git failure must refuse every path, got %+v", a.Rel, a)
		}
	}
}

func TestIngestGuardTooLarge(t *testing.T) {
	root := guardRepo(t, nil)
	write := func(rel string, n int) {
		mustWrite(t, filepath.Join(root, rel), strings.Repeat("a", n), 0o644)
	}
	write("big.txt", MaxAdmitBytes+1)
	write("edge.txt", MaxAdmitBytes)
	wantRefused(t, root, "big.txt", ReasonTooLarge)
	if a := admitOne(t, root, "edge.txt"); !a.Admitted || len(a.Content) != MaxAdmitBytes {
		t.Errorf("a file of exactly MaxAdmitBytes must be admitted whole: %v %d", a.Admitted, len(a.Content))
	}
}

func TestIngestGuardFIFORefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on windows")
	}
	root := guardRepo(t, nil)
	if out, err := exec.Command("mkfifo", filepath.Join(root, "pipe.go")).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v %s", err, out)
	}
	done := make(chan Admission, 1)
	go func() { a, _ := Admit(context.Background(), root, "pipe.go"); done <- a }()
	select {
	case a := <-done:
		if a.Admitted || a.Reason != ReasonExcluded {
			t.Fatalf("FIFO not refused as excluded: %+v", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Admit blocked on a FIFO")
	}
}

func TestIngestGuardEntropyOnlyRefused(t *testing.T) {
	root := guardRepo(t, map[string]string{"sum.txt": "checksum 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0 verified\n"})
	a := wantRefused(t, root, "sum.txt", ReasonSecret)
	if a.PatternClass != string(secrets.ClassHighEntropy) {
		t.Errorf("PatternClass = %q, want %q", a.PatternClass, secrets.ClassHighEntropy)
	}
}

func TestAdmitBatchRefusesOversizedBatch(t *testing.T) {
	root := guardRepo(t, nil)
	rels := make([]string, MaxAdmitBatch+1)
	for i := range rels {
		rels[i] = fmt.Sprintf("f%d.go", i)
	}
	out, err := AdmitBatch(context.Background(), root, rels)
	if kind, ok := cascade.KindOf(err); out != nil || !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("513 paths: out=%d kind=%v ok=%v err=%v, want KindInvalidInput", len(out), kind, ok, err)
	}
	out, err = AdmitBatch(context.Background(), root, rels[:MaxAdmitBatch])
	if err != nil || len(out) != MaxAdmitBatch {
		t.Fatalf("512 paths: %d results, err=%v", len(out), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AdmitBatch(ctx, root, rels[:1]); err == nil {
		t.Error("a canceled context must be an error, not an admission")
	}
}

// TestIngestGuardGlobNamedPathStillJudged: check-ignore reads a path as a
// pattern and skips tracked files, so --no-index is needed (see the guard).
func TestIngestGuardGlobNamedPathStillJudged(t *testing.T) {
	files := map[string]string{".gitignore": "*.secret\n", "a.secret": "x\n"}
	if runtime.GOOS != "windows" { // windows forbids "*" in a file name
		files["*.secret"] = "y\n"
	}
	root := guardRepo(t, files)
	for _, args := range [][]string{{"add", "-f", "a.secret"}, {"-c", "user.name=t", "-c", "user.email=t@e.co", "commit", "-q", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wantRefused(t, root, "a.secret", ReasonIgnored)
	if runtime.GOOS != "windows" {
		wantRefused(t, root, "*.secret", ReasonIgnored)
	}
}

// TestIngestGuardSwapAfterLstatRefused swaps the file between lstat and open
// for a symlink (SameFile) and a FIFO (O_NONBLOCK, fstat regular).
func TestIngestGuardSwapAfterLstatRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and FIFO swaps are POSIX")
	}
	root := guardRepo(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n", "c.go": "package c\n"})
	t.Cleanup(func() { beforeOpen = func(string) {} })
	swap := func(target string, mk func(full string) error) {
		beforeOpen = func(full string) {
			if filepath.Base(full) != target {
				return
			}
			if err := os.Remove(full); err != nil {
				t.Errorf("removing %s: %v", target, err)
			} else if err = mk(full); err != nil {
				t.Errorf("swapping %s: %v", target, err)
			}
		}
	}
	swap("b.go", func(full string) error { return os.Symlink(filepath.Join(filepath.Dir(full), "c.go"), full) })
	wantRefused(t, root, "b.go", ReasonExcluded)
	swap("a.go", func(full string) error { return exec.Command("mkfifo", full).Run() })
	done := make(chan struct{})
	go func() { wantRefused(t, root, "a.go", ReasonExcluded); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Admit blocked on a FIFO swapped in after lstat")
	}
}
