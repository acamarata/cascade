// Package retrieval (this file) tests that IngestGitRepo admits every
// tracked path through the repo ingest guard: a tracked .env and a
// secret-bearing source file never reach a chunk, the chunked bytes are the
// scanned bytes, and a large repository is admitted in bounded slices. All
// secret-shaped fixtures are synthetic and assembled at run time.
//
// SPORT: internal.retrieval.IngestGitRepo/ADMISSION test coverage.
package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// admitCanary is a synthetic vendor-key-shaped value (never a real key).
func admitCanary() string { return "sk" + "-ant-" + strings.Repeat("Zq7", 10) }

// commitAll writes files into a fresh real git repository and commits them.
func commitAll(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "fixture")
	return dir
}

func TestIngestGitRepoAdmitsThroughGuard(t *testing.T) {
	root := commitAll(t, map[string]string{
		".env":      "SERVICE_TOKEN=" + admitCanary() + "\n",
		"cfg.go":    "package cfg\n\nconst key = \"" + admitCanary() + "\"\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"util/u.go": "package util\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	_, chunks, refused, err := IngestGitRepo(context.Background(), root, "project/cascade", corpus.TrustTrusted)
	if err != nil {
		t.Fatalf("IngestGitRepo: %v", err)
	}
	paths := map[string]bool{}
	for _, ch := range chunks {
		paths[ch.Path] = true
		if strings.Contains(string(ch.Content), "Zq7Zq7") {
			t.Fatalf("a chunk of %q carries the canary", ch.Path)
		}
	}
	if paths[".env"] || paths["cfg.go"] || !paths["main.go"] || !paths["util/u.go"] {
		t.Fatalf("chunked paths = %v, want main.go and util/u.go only", paths)
	}
	got := map[string]string{}
	for _, r := range refused {
		got[r.Rel] = r.Reason
		if r.Content != nil || r.Admitted {
			t.Errorf("refusal %+v carries content or is marked admitted", r)
		}
	}
	if len(got) != 2 || got[".env"] != repo.ReasonExcluded || got["cfg.go"] != repo.ReasonSecret {
		t.Fatalf("refusals = %v, want .env excluded-path and cfg.go secret-detected", got)
	}
	blob, _ := json.Marshal(refused)
	if strings.Contains(string(blob), "Zq7Zq7") {
		t.Fatalf("a refusal carries the canary: %s", blob)
	}
}

func TestIngestGitRepoUsesAdmittedBytes(t *testing.T) {
	root := commitAll(t, map[string]string{"main.go": "package main\n\nfunc Original() {}\n"})
	orig := admitBatch
	t.Cleanup(func() { admitBatch = orig })
	admitBatch = func(ctx context.Context, r string, rels []string) ([]repo.Admission, error) {
		out, err := orig(ctx, r, rels)
		// The file is swapped after the scan, before the chunking.
		swapped := "package main\n\nfunc Swapped() { _ = \"" + admitCanary() + "\" }\n"
		if werr := os.WriteFile(filepath.Join(r, "main.go"), []byte(swapped), 0o644); werr != nil {
			t.Fatal(werr)
		}
		return out, err
	}
	_, chunks, refused, err := IngestGitRepo(context.Background(), root, "project/cascade", corpus.TrustTrusted)
	if err != nil || len(refused) != 0 {
		t.Fatalf("IngestGitRepo: err=%v refused=%+v", err, refused)
	}
	var all strings.Builder
	for _, ch := range chunks {
		all.Write(ch.Content)
	}
	if !strings.Contains(all.String(), "Original") || strings.Contains(all.String(), "Swapped") {
		t.Fatalf("indexed bytes are not the scanned bytes: %q", all.String())
	}
}

func TestIngestGitRepoAdmitsInBoundedSlices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the git call counter is a POSIX shell shim")
	}
	files := map[string]string{}
	for i := 0; i < 1300; i++ {
		files[fmt.Sprintf("src/f%04d.go", i)] = fmt.Sprintf("package src\n\nfunc F%04d() {}\n", i)
	}
	root := commitAll(t, files)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found: %v", err)
	}
	shimDir, logPath := t.TempDir(), filepath.Join(t.TempDir(), "calls.log")
	shim := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = check-ignore ] && echo x >> '" + logPath + "'; done\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	orig := admitBatch
	t.Cleanup(func() { admitBatch = orig })
	var sizes []int
	var prev []repo.Admission
	admitBatch = func(ctx context.Context, r string, rels []string) ([]repo.Admission, error) {
		for _, a := range prev {
			if a.Content != nil {
				t.Errorf("the previous slice still holds %q's content when the next slice is admitted", a.Rel)
				break
			}
		}
		sizes = append(sizes, len(rels))
		out, err := orig(ctx, r, rels)
		prev = out
		return out, err
	}
	_, chunks, refused, err := IngestGitRepo(context.Background(), root, "project/cascade", corpus.TrustTrusted)
	if err != nil || len(refused) != 0 || len(chunks) < 1300 {
		t.Fatalf("IngestGitRepo: err=%v refused=%d chunks=%d", err, len(refused), len(chunks))
	}
	if fmt.Sprint(sizes) != "[512 512 276]" {
		t.Errorf("slice sizes = %v, want [512 512 276]", sizes)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no check-ignore call was recorded: %v", err)
	}
	if n := strings.Count(string(logged), "x\n"); n != 3 {
		t.Errorf("check-ignore ran %d times, want exactly 3", n)
	}
}
