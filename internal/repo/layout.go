package repo

// Purpose: repo-level facts that are not language-specific: the bounded
//   tree walk (LayoutFacts), CI system presence, and existing AI-harness
//   file presence.
// Constraints: the walk never follows a symlink (a symlinked directory is
//   skipped, never recursed into -- this is what stops a symlink loop and
//   what stops a symlink escaping root), and it is bounded in both depth
//   and file count so a pathological tree cannot make the scan run
//   unboundedly. An unreadable entry is skipped, never a fatal error, and
//   never silently miscounted -- it simply does not contribute a
//   dir/file count. Two repo-relative paths that differ only by case are
//   recorded in LayoutFacts.CaseClash rather than one silently shadowing
//   the other (relevant on a case-insensitive filesystem such as default
//   macOS/Windows).
// SPORT: repo/layout-facts/ADD (P1-E33-W7-S67-T1).

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// walkMaxDepth and walkMaxEntries bound the tree walk. 64 directory
// levels and 200,000 entries are far beyond any real source tree this
// project scans, and comfortably below what would make a single scan
// noticeably slow; a tree that exceeds either bound sets
// LayoutFacts.Truncated rather than continuing unboundedly.
const (
	walkMaxDepth   = 64
	walkMaxEntries = 200000
)

// skippedTopDirs are directories the walk never descends into: version
// control internals and common dependency/build caches that are large,
// irrelevant to layout facts, and (for .git) not a source tree at all.
var skippedTopDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "target": true, ".cover": true,
}

// walkRepo performs the bounded, symlink-safe walk and returns the raw
// counts plus every regular file's repo-relative path (used by
// caseClashes below; layout.go's own callers only need the counts, but
// keeping the path list here means the walk itself -- the part the traps
// are about -- runs exactly once per scan).
func walkRepo(root string) (dirCount, fileCount, maxDepth int, truncated bool, paths []string, err error) {
	return walkRepoWithBudget(root, walkMaxEntries)
}

// walkState accumulates walkRepoWithBudget's counters across the
// filepath.WalkDir callback, split into its own type (rather than one
// long closure) purely to keep the per-entry visitor under the 50-line
// function cap.
type walkState struct {
	root       string
	maxEntries int
	total      int
	dirCount   int
	fileCount  int
	maxDepth   int
	truncated  bool
	paths      []string
}

// visit is the filepath.WalkDir callback body.
func (w *walkState) visit(path string, d fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		// An unreadable entry (permission denied, vanished mid-walk) is
		// skipped, not fatal: continue past it rather than aborting the
		// whole scan on one bad entry.
		if d != nil && d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	rel, relErr := filepath.Rel(w.root, path)
	if relErr != nil {
		return nil
	}
	depth := 0
	if rel != "." {
		depth = strings.Count(rel, string(filepath.Separator)) + 1
	}
	if depth > w.maxDepth {
		w.maxDepth = depth
	}
	if depth > walkMaxDepth {
		w.truncated = true
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	w.total++
	if w.total > w.maxEntries {
		w.truncated = true
		return filepath.SkipAll
	}

	info, infoErr := d.Info()
	isSymlink := infoErr == nil && info.Mode()&fs.ModeSymlink != 0

	if d.IsDir() {
		return w.visitDir(rel, d.Name(), isSymlink)
	}
	if isSymlink {
		// A symlinked file is counted as present but never opened or
		// followed.
		return nil
	}
	w.fileCount++
	if rel != "." {
		w.paths = append(w.paths, filepath.ToSlash(rel))
	}
	return nil
}

// visitDir handles the directory branch of visit.
func (w *walkState) visitDir(rel, name string, isSymlink bool) error {
	if rel != "." && skippedTopDirs[name] {
		return filepath.SkipDir
	}
	if isSymlink {
		// Never follow a symlinked directory: this is what prevents both
		// a symlink loop and a symlink escaping root, without needing to
		// resolve and compare real paths at every step.
		return filepath.SkipDir
	}
	if rel != "." {
		// The root itself is the tree being described, not an entry
		// within it.
		w.dirCount++
	}
	return nil
}

// walkRepoWithBudget is walkRepo with an injectable entry-count budget,
// so layout_test.go can exercise the truncation branch without creating
// walkMaxEntries real files on disk.
func walkRepoWithBudget(root string, maxEntries int) (dirCount, fileCount, maxDepth int, truncated bool, paths []string, err error) {
	w := &walkState{root: root, maxEntries: maxEntries}
	if walkErr := filepath.WalkDir(root, w.visit); walkErr != nil {
		return 0, 0, 0, false, nil, cascade.Wrap(cascade.KindUnavailable, walkErr, "repo: walk tree")
	}
	return w.dirCount, w.fileCount, w.maxDepth, w.truncated, w.paths, nil
}

// caseClashes finds repo-relative paths that collide when lower-cased --
// the case a case-insensitive filesystem would already have refused to
// hold as two distinct files, but which a case-sensitive filesystem (or a
// tree produced on one, then copied) can genuinely contain. Reported
// paths are sorted only by discovery order (deterministic given a
// deterministic paths slice), never re-sorted, so two scans of an
// unchanged tree produce an identical order.
func caseClashes(paths []string) []string {
	seen := map[string]string{}
	var clashes []string
	for _, p := range paths {
		lower := strings.ToLower(p)
		if first, ok := seen[lower]; ok {
			if first != p {
				clashes = append(clashes, first, p)
			}
			continue
		}
		seen[lower] = p
	}
	return clashes
}

// ScanLayout produces LayoutFacts for root.
func ScanLayout(root string) (LayoutFacts, error) {
	if root == "" {
		return LayoutFacts{}, cascade.New(cascade.KindInvalidInput, "repo: ScanLayout requires a non-empty root")
	}
	dirCount, fileCount, maxDepth, truncated, paths, err := walkRepo(root)
	if err != nil {
		return LayoutFacts{}, err
	}
	return LayoutFacts{
		DirCount:  dirCount,
		FileCount: fileCount,
		MaxDepth:  maxDepth,
		Truncated: truncated,
		CaseClash: caseClashes(paths),
	}, nil
}

// ciMarkers maps a repo-relative marker path to the CIFacts field it
// sets. .github/workflows is a directory, checked for existence via
// evidenceDirExists rather than evidenceExists.
var ciDirMarkers = map[string]string{".github/workflows": "github"}
var ciFileMarkers = map[string]string{".gitlab-ci.yml": "gitlab", ".circleci/config.yml": "circleci"}

// ScanCI reports which CI system markers exist at root.
func ScanCI(root string) (CIFacts, error) {
	var facts CIFacts
	for dir := range ciDirMarkers {
		ok, err := evidenceDirExists(root, dir)
		if err != nil {
			return CIFacts{}, err
		}
		if ok {
			facts.GitHubActions = true
		}
	}
	for file, kind := range ciFileMarkers {
		ok, err := evidenceExists(root, file)
		if err != nil {
			return CIFacts{}, err
		}
		if ok {
			switch kind {
			case "gitlab":
				facts.GitLabCI = true
			case "circleci":
				facts.CircleCI = true
			}
		}
	}
	return facts, nil
}

// ScanHarness reports which existing AI-harness files/dirs exist at root.
func ScanHarness(root string) (HarnessFacts, error) {
	var facts HarnessFacts
	var err error
	if facts.ClaudeMD, err = evidenceExists(root, "CLAUDE.md"); err != nil {
		return HarnessFacts{}, err
	}
	if facts.AgentsMD, err = evidenceExists(root, "AGENTS.md"); err != nil {
		return HarnessFacts{}, err
	}
	if facts.ClaudeDir, err = evidenceDirExists(root, ".claude"); err != nil {
		return HarnessFacts{}, err
	}
	return facts, nil
}

// evidenceDirExists reports whether name exists as a directory directly
// under root (never following a symlink).
func evidenceDirExists(root, name string) (bool, error) {
	info, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "repo: stat %s", name)
	}
	return info.IsDir(), nil
}
