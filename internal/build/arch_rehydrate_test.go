// Package build (this file): the executor-only gate for secret
// rehydration. Putting a raw credential back into content is permitted at
// exactly one boundary, immediately before an action runs, and the rule
// is enforced here rather than left as prose in a doc comment: a Rehydrate
// call in middleware, a pipeline stage, a tool handler, a log path or a
// storage write would put the value somewhere it can be stored or
// forwarded, and every one of those reads as ordinary code at review time.
//
// Two rules:
//
//  1. Rehydrate is called only from internal/secrets (its own package)
//     and from the conductor executor package that dispatches actions.
//  2. internalGet, the unelevated vault read rehydration uses, exists
//     only inside internal/secrets. It is unexported, so this is already
//     true of any Go program that compiles; the assertion is here so that
//     exporting it becomes a RED test rather than a quiet widening.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rehydrateCallerAllowlist is the set of directories, relative to the
// module root, whose files may call Rehydrate.
var rehydrateCallerAllowlist = []string{
	filepath.Join("internal", "secrets"),
	filepath.Join("internal", "conductor", "executor"),
}

// TestArchRehydrateCallers asserts rule 1 on the real tree.
func TestArchRehydrateCallers(t *testing.T) {
	root := archModuleRoot(t)
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		scanRehydrateCallers(t, root, filepath.Join(root, dir))
	}
}

// scanRehydrateCallers walks tree and fails on a call outside the
// allowlist. Test files are skipped: a test that drives the mechanism is
// not a production caller, and the allowlist is about where the VALUE can
// travel in a shipped binary.
func scanRehydrateCallers(t *testing.T, root, tree string) {
	t.Helper()
	err := filepath.WalkDir(tree, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // path comes from the module tree
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(string(data), ".Rehydrate(") && !strings.Contains(string(data), " Rehydrate(") {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		if !rehydrateCallerAllowed(rel) {
			t.Fatalf("%s calls Rehydrate; rehydration is executor-only and this package is not %v",
				path, rehydrateCallerAllowlist)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", tree, err)
	}
}

// rehydrateCallerAllowed reports whether dir is on the allowlist.
func rehydrateCallerAllowed(dir string) bool {
	for _, allowed := range rehydrateCallerAllowlist {
		if dir == allowed || strings.HasPrefix(dir, allowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// TestArchRehydrateCallersDetectsViolation is the seeded-violation half:
// the same predicate over a directory that is NOT on the allowlist must
// report it. Without this the rule above would also pass if the scan
// never matched anything.
func TestArchRehydrateCallersDetectsViolation(t *testing.T) {
	if rehydrateCallerAllowed(filepath.Join("internal", "hooks")) {
		t.Fatal("internal/hooks must not be an allowed Rehydrate caller")
	}
	if rehydrateCallerAllowed(filepath.Join("internal", "secretsish")) {
		t.Fatal("the allowlist must match whole path segments, not string prefixes")
	}
	if !rehydrateCallerAllowed(filepath.Join("internal", "secrets")) {
		t.Fatal("internal/secrets must be an allowed Rehydrate caller")
	}
}

// TestArchInternalGetCallers asserts rule 2: the unelevated vault read
// appears only inside internal/secrets.
func TestArchInternalGetCallers(t *testing.T) {
	root := archModuleRoot(t)
	allowed := filepath.Join(root, "internal", "secrets")
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		walkErr := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			// Test files are skipped for the same reason rule 1 skips
			// them, and because this gate's own source names the symbol.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path) //nolint:gosec // path comes from the module tree
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(data), "internalGet(") && !strings.HasPrefix(path, allowed+string(filepath.Separator)) {
				t.Fatalf("%s references internalGet; the unelevated vault read never leaves internal/secrets", path)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walking %s: %v", dir, walkErr)
		}
	}
}
