// Package goldencheck plans and aggregates the golden-parity CI lane.
// Inputs: a module tree, the committed owner table, `go test -json` output.
// Outputs: the run plan (one row per golden root) and one JSON report.
// Constraints: no subprocess; the CI job runs the rows, this package only
// plans and aggregates, so no new exec surface exists in product packages.
package goldencheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	goldenDirName    = "v1-goldens"
	migrationDirName = "migration"
	claudeCodeSuffix = "testdata/goldens/claude-code"
)

// skipDirs are never descended into: VCS state, planning state and caches.
var skipDirs = map[string]bool{
	".git": true, ".claude": true, ".cascade": true, ".opencode": true, "node_modules": true,
}

// DiscoverRoots returns the sorted, module-relative slash paths of every
// golden root under moduleRoot: each v1-goldens directory, each
// v1-goldens/migration sub-root, and the claude-code replay directory under
// testdata/goldens. A v1-goldens directory whose only entry is a migration
// sub-root is container-only and is not itself a root.
func DiscoverRoots(moduleRoot string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if skipDirs[d.Name()] && path != moduleRoot {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.Name() == goldenDirName:
			found, err := goldenDirRoots(path, rel)
			roots = append(roots, found...)
			if err != nil {
				return err
			}
			return filepath.SkipDir
		case rel == claudeCodeSuffix || strings.HasSuffix(rel, "/"+claudeCodeSuffix):
			roots = append(roots, rel)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover golden roots under %s: %w", moduleRoot, err)
	}
	sort.Strings(roots)
	return roots, nil
}

// goldenDirRoots expands one v1-goldens directory into its roots.
func goldenDirRoots(abs, rel string) ([]string, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	sub, hasMigration := "", false
	for _, e := range entries {
		if e.IsDir() && e.Name() == migrationDirName {
			sub, hasMigration = rel+"/"+migrationDirName, true
		}
	}
	if hasMigration && len(entries) == 1 {
		return []string{sub}, nil
	}
	if hasMigration {
		return []string{rel, sub}, nil
	}
	return []string{rel}, nil
}
