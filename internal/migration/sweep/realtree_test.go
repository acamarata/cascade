// Purpose: the default-lane drift gate — proves the committed
// docs/migration outputs still match a fresh sweep of the real tracked
// tree, so a ticket that adds or removes a v1-term mention without
// regenerating the two committed outputs fails CI immediately rather than
// silently drifting. Runs with no build tags (C13: evidence from an
// execution the gate ran on the current tree). Test-only os/exec use is
// allowed (egress_scan.go skips _test.go); production code in this
// package never shells out.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realModuleRoot resolves the module root via `git rev-parse
// --show-toplevel`, run with cmd.Dir set explicitly to the package
// directory (`go test` makes it the working directory), so the result does
// not depend on where `go test` was invoked from. It fails, never skips,
// when git is unavailable.
func realModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel") //nolint:gosec // test-only, offline
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel (from %s): %v", wd, err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git rev-parse --show-toplevel returned an empty root")
	}
	return root
}

// realTrackedFiles runs `git ls-files -z` with cmd.Dir = root (never the
// package dir, which would list only this subtree with paths relative to
// it) and asserts the result contains go.mod and at least one file.
func realTrackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z") //nolint:gosec // test-only, offline
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files -z (dir=%s): %v", root, err)
	}
	var files []string
	for _, part := range bytes.Split(out, []byte{0}) {
		if len(part) > 0 {
			files = append(files, string(part))
		}
	}
	if len(files) == 0 {
		t.Fatal("git ls-files -z returned no files")
	}
	hasGoMod := false
	for _, f := range files {
		if f == "go.mod" {
			hasGoMod = true
			break
		}
	}
	if !hasGoMod {
		t.Fatal("tracked file list does not contain go.mod; wrong root?")
	}
	return files
}

// TestInventoryMatchesRealTree is the default-lane drift gate: it
// reproduces a full sweep run against the real tracked tree and asserts
// the committed outputs are byte-identical to what that run would
// (re)generate. A term hit the committed inventory does not list, or a
// listed pair whose file no longer holds the term, fails this test naming
// the (term, path) row; an edit above or below an
// existing hit line never breaks it (the committed rows carry no line
// numbers).
func TestInventoryMatchesRealTree(t *testing.T) {
	root, _, rows := realTreeRows(t)
	assertMatchesCommittedInventory(t, root, rows)
}

// realTreeRows runs the full sweep over the real tracked tree, exactly as
// the regenerate command would, and returns the module root, the full
// tracked list (unfiltered) and the classified rows. Every exit-2 shaped
// condition (missing replacement, missing anchor, stale planned term,
// stale provenance ref) fails the calling test.
func realTreeRows(t *testing.T) (string, []string, []Row) {
	t.Helper()
	root := realModuleRoot(t)
	files := realTrackedFiles(t, root)

	termsPath := filepath.Join(root, "internal", "migration", "sweep", "testdata", "v1-terms.json")
	provPath := filepath.Join(root, "internal", "migration", "sweep", "testdata", "provenance-refs.json")
	terms, err := LoadTermFile(termsPath)
	if err != nil {
		t.Fatalf("LoadTermFile: %v", err)
	}
	provRefs, err := LoadProvenanceRefs(provPath)
	if err != nil {
		t.Fatalf("LoadProvenanceRefs: %v", err)
	}
	tracked := filterTracked(files)
	if len(tracked) == 0 {
		t.Fatalf("no walked file after filtering %d tracked paths to the sweep roots", len(files))
	}
	hits, err := (Sweeper{Root: root, Terms: terms, Files: tracked}).Scan()
	if err != nil {
		t.Fatalf("Scan: %v (a missing replacement, missing anchor, or stale planned term — exit-2 shaped)", err)
	}
	if stale := staleProvenanceRefs(provRefs, hits); len(stale) > 0 {
		t.Fatalf("stale provenance-refs entries with no matching real-tree hit: %v", stale)
	}
	provSet := make(map[string]bool, len(provRefs))
	for _, r := range provRefs {
		provSet[r.Path+"|"+r.Term] = true
	}
	rows, err := buildRows(hits, provSet, &Resolver{Root: root})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	return root, files, rows
}

// assertMatchesCommittedInventory renders rows the same way a real run
// would and compares the result against the committed docs/migration
// outputs. A drift names each (term, path) row that is missing from, or
// extra in, the committed inventory, and each unresolved.json entry that
// differs, rather than dumping whole files.
func assertMatchesCommittedInventory(t *testing.T, root string, rows []Row) {
	t.Helper()
	gotMD := filepath.Join(t.TempDir(), "v1-pointer-inventory.md")
	if err := WriteMarkdown(rows, gotMD); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	gotJSON := filepath.Join(t.TempDir(), "unresolved.json")
	if err := WriteUnresolvedJSON(rows, gotJSON); err != nil {
		t.Fatalf("WriteUnresolvedJSON: %v", err)
	}
	const regen = "regenerate with `go run ./internal/migration/sweep --files <(git ls-files -z)` and commit"
	for _, pair := range [][2]string{
		{filepath.Join(root, "docs", "migration", "v1-pointer-inventory.md"), gotMD},
		{filepath.Join(root, "docs", "migration", "unresolved.json"), gotJSON},
	} {
		want, got := readFile(t, pair[0]), readFile(t, pair[1])
		if bytes.Equal(want, got) {
			continue
		}
		name := filepath.Base(pair[0])
		missing, extra := lineDiff(want, got)
		for _, ln := range missing {
			t.Errorf("%s: committed row not produced by a fresh sweep: %s", name, ln)
		}
		for _, ln := range extra {
			t.Errorf("%s: fresh sweep row missing from the committed file: %s", name, ln)
		}
		if len(missing) == 0 && len(extra) == 0 {
			t.Errorf("%s: same rows, different bytes (order or header drift)", name)
		}
		t.Errorf("%s is stale: %s", name, regen)
	}
}

// readFile reads p or fails the test naming it.
func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p) //nolint:gosec // test-only path under the module root or t.TempDir
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return data
}

// lineDiff returns the non-blank lines only in want (missing from the
// fresh output) and only in got (extra in the fresh output). For the
// inventory each such line is one "| term | path | ... |" row, so the
// message names the (term, path) pair directly.
func lineDiff(want, got []byte) (missing, extra []string) {
	count := map[string]int{}
	for _, ln := range strings.Split(string(want), "\n") {
		if strings.TrimSpace(ln) != "" {
			count[ln]++
		}
	}
	for _, ln := range strings.Split(string(got), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if count[ln] > 0 {
			count[ln]--
			continue
		}
		extra = append(extra, ln)
	}
	for _, ln := range strings.Split(string(want), "\n") {
		if count[ln] > 0 {
			count[ln]--
			missing = append(missing, ln)
		}
	}
	return missing, extra
}
