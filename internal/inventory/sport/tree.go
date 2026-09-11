// Purpose: the generation-time half — resolve the real git-tracked .go
//
//	file list and build a Registry against it. Only called by
//	internal/inventory/gen (which writes registry.json) and this
//	package's own idempotency test; the production binary never calls
//	this (it reads the embedded artifact instead — embed.go).
//
// Constraints: fails closed on a malformed line (see doc.go) — a
//
//	generation run that hit one must not silently produce a registry
//	missing that entity.
//
// SPORT: internal.inventory.sport.ComputeRegistry/ADDED.

package sport

import (
	"fmt"
	"os/exec"
	"strings"
)

// ComputeRegistry scans every git-tracked *.go file under repoRoot and
// returns the deduplicated Registry, or an error if any marker line was
// malformed (ParseError) or git/file I/O failed.
func ComputeRegistry(repoRoot, generatedAt string) (Registry, error) {
	files, err := gitTrackedGoFiles(repoRoot)
	if err != nil {
		return Registry{}, err
	}
	raw, errs, err := ScanFiles(repoRoot, files)
	if err != nil {
		return Registry{}, err
	}
	if len(errs) > 0 {
		return Registry{}, fmt.Errorf("sport: %d malformed SPORT line(s), first: %w", len(errs), errs[0])
	}
	return Dedupe(raw, generatedAt), nil
}

// gitTrackedGoFiles duplicates internal/inventory/tree.go's helper of the
// same name: this package is linked into the production CLI binary (via
// cmd/cascade's doctor sport view), and shelling out to git must never be
// part of that path.
func gitTrackedGoFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" || strings.HasPrefix(f, seededViolationDir) {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// seededViolationDir holds the deliberately-broken fixtures internal/build's
// gates use to prove they can turn red. One of them carries a malformed
// SPORT marker on purpose, so scanning it as ordinary source makes this
// package report a malformed line that is doing exactly its job: the gate
// then fails on its own evidence and the registry cannot be generated.
//
// Worth knowing why this was missed: the scan reads `git ls-files`, and the
// fixture was still UNTRACKED while the gate was written and verified. It
// became tracked in the same commit that shipped the gate, so the check was
// green every local run beforehand and red the first time CI saw it. A gate
// verified against a working tree is not verified against the tree CI sees.
const seededViolationDir = "internal/build/testdata/seeded-violations/"
