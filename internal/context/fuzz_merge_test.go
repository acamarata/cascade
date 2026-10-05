package context

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Fuzz target for the instruction merge model. Kept in its own file per the
// module's existing convention (internal/runtime/fuzz_test.go,
// internal/secrets/fuzz_env_test.go) and to stay under Art.10.3's 300-line
// file cap.

// FuzzMergeTiers drives the section splitter and the precedence pass with
// arbitrary tier bodies. Beyond "does not panic" it re-derives the expected
// winner for every surviving heading from the SPEC rule (lowest ordinal
// among the tiers that define it) and requires the implementation to agree.
// Seeds come from internal/testdata/fuzz/context/ per 06-FORGE-SPEC.md §5.7.
func FuzzMergeTiers(f *testing.F) {
	for _, seed := range fuzzMergeSeeds(f) {
		f.Add(seed, seed, "")
		f.Add("", seed, seed)
	}
	f.Add("## A\n1\n", "## A\n2\n", "## B\n3\n")

	f.Fuzz(func(t *testing.T, gci, ppi, pai string) {
		tiers := []TierRecord{rec(TierGCI, 0, gci), rec(TierPPC, 2, ppi), rec(TierPAC, 4, pai)}
		merged, err := MergeTiers(tiers)
		if err != nil {
			return // rejected input: fail-closed is a valid outcome.
		}
		for _, s := range merged.Sections {
			if s.Heading == "" {
				continue
			}
			want := TierRole(0)
			for _, r := range tiers {
				for _, blk := range splitSections(r.Content) {
					if blk.heading == s.Heading && (want == 0 || r.Role < want) {
						want = r.Role
					}
				}
			}
			if s.Role != want {
				t.Fatalf("heading %q won by %s, spec says %s", s.Heading, s.Role, want)
			}
		}
	})
}

// fuzzMergeSeeds loads the curated seed bodies for FuzzMergeTiers.
func fuzzMergeSeeds(f *testing.F) []string {
	f.Helper()
	dir := filepath.Join("..", "testdata", "fuzz", "context")
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Fatalf("reading fuzz seed corpus %s: %v", dir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), err)
		}
		seeds = append(seeds, string(raw))
	}
	if len(seeds) == 0 {
		f.Fatal("fuzz seed corpus is empty (fail closed: a silently empty corpus fuzzes nothing)")
	}
	return seeds
}

// Tier read failures and git edge cases, kept here for the 300-line cap.

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func TestLoadTierReadFailureAndOversizedRead(t *testing.T) {
	dir := resolvedTempDir(t)
	plantTier(t, dir, "tiny")
	t.Cleanup(func() { wrapTierReader = func(r io.Reader) io.Reader { return r } })
	wrapTierReader = func(io.Reader) io.Reader { return failingReader{} }
	_, err := loadTier(TierPRC, dir, 3)
	wantMsg := "context: read tier file " + filepath.Join(dir, tierDirName, tierFileName)
	if !cascade.HasKind(err, cascade.KindUnavailable) || err == nil || !strings.Contains(err.Error(), wantMsg) || !strings.Contains(err.Error(), "injected read failure") {
		t.Fatalf("loadTier = %v, want KindUnavailable containing %q and the cause", err, wantMsg)
	}
	wrapTierReader = func(r io.Reader) io.Reader {
		return io.MultiReader(r, strings.NewReader(strings.Repeat("x", maxTierBytes)))
	}
	rec, err := loadTier(TierPRC, dir, 3)
	if err != nil || !rec.Absent || rec.Content != "" || len(rec.Findings) != 1 || rec.Findings[0] != FindingTierTooLarge {
		t.Fatalf("loadTier = %+v, %v; want Absent with exactly FindingTierTooLarge", rec, err)
	}
}

func TestGitRootEmptyOutputIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cwd := resolvedTempDir(t)
	root, finding := gitRoot(context.Background(), cwd)
	if root != cwd || finding != FindingGitUnavailable {
		t.Fatalf("gitRoot = %q, %q; want %q, %q", root, finding, cwd, FindingGitUnavailable)
	}
}

func TestAlignCwdKeepsCwdWhenResolutionFailsOrLeavesAnchor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	root := resolvedTempDir(t)
	anchor := filepath.Join(root, "repo")
	missing := filepath.Join(root, "absent", "x")
	if got := alignCwd(anchor, missing); got != missing {
		t.Errorf("alignCwd(missing cwd) = %q, want %q", got, missing)
	}
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if got := alignCwd(anchor, link); got != link {
		t.Errorf("alignCwd(link outside anchor) = %q, want %q", got, link)
	}
}
