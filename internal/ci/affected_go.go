// Purpose (this file): the Go-stack Affected path -- a real `go list`
// subprocess builds each local package's direct-import edges, this file
// walks the reverse of that graph to collect every package transitively
// affected by a changed file, and FuzzAffectedGoList (affected_fuzz_test.go)
// fuzzes the line parser the subprocess's own output is fed through.
//
// Inputs: worktreeRoot (a real Go module checkout) and a changed-path
// list (repository-relative, as ChangedPaths returns them).
// Outputs: the []Target set of local import paths whose transitive
// imports include at least one changed path's owning package.
// Constraints: two real `go list` subprocess shapes only, no test double
// for the Go path (Art.2): `go list -f '{{.ImportPath}}|{{join .Imports
// ","}},{{join .TestImports ","}},{{join .XTestImports ","}}' ./...` once
// per call (the graph, folding production AND test import edges into one
// per-package edge list -- D1/S-65.T1 CR round: a package imported only
// by another package's _test.go file must still be selected when that
// import target changes), and `go list -f {{.ImportPath}} <dir>` once per
// distinct changed directory (resolving that directory's real import path
// -- avoids hand-parsing go.mod's module line, which would itself be
// exactly the "hand-written graph the code merely echoes" this ticket's
// spec warns against). GOTOOLCHAIN=local is forced on both so a fixture
// whose go.mod names a `go` directive the installed toolchain already
// satisfies never attempts a network toolchain fetch (06 §5.15: no
// network in a unit test).
// Deviation (recorded): full_desc's prose names a single-field `go list
// -deps -f {{.ImportPath}} ./...` template. `-deps` alone cannot express
// per-package edges (it only flattens the iteration set, discarding which
// package pulled in which dependency); this file instead adds three
// fields (`.Imports`/`.TestImports`/`.XTestImports`, each `join`ed) to the
// SAME single `./...` call, giving real DIRECT-import edges (production
// and test alike) in one subprocess, and builds the TRANSITIVE reverse
// graph locally -- functionally what "parse import-path output, build
// reverse-import graph" (this ticket's own task list) describes, without
// an O(local-package-count) subprocess fan-out.
// The changed-path -> owning-package fail-closed resolution
// (changedOwningPackages and its helpers) lives in its own sibling file,
// affected_go_mapping.go, to stay under Art.10.3's 300-line file cap.
// SPORT: internal.ci.parseImportGraph/ADDED (P1-E32-W6-S65-T1).

package ci

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// goListImportGraphTemplate lists, for every local package under
// worktreeRoot, its own import path and its DIRECT imports (local and
// external alike -- external entries are simply never found among the
// local vertex set when the reverse walk runs, so they need no separate
// filtering pass).
const goListImportGraphTemplate = `{{.ImportPath}}|{{join .Imports ","}},{{join .TestImports ","}},{{join .XTestImports ","}}`

// goListEnv returns environ with GOTOOLCHAIN=local appended, so a go
// list/go build subprocess never attempts a network toolchain
// resolution against a fixture module's `go` directive.
func goListEnv() []string {
	return append(append([]string{}, os.Environ()...), "GOTOOLCHAIN=local")
}

// affectedGoTargets is the Go-stack Affected implementation: build the
// direct-import graph, resolve each changed path to its owning local
// package, then collect every local package whose transitive imports
// include one of those owning packages (including the owning package
// itself -- a package always "transitively imports" itself for this
// purpose, matching "isolated" and "cross-cutting" acceptance cases
// alike).
func affectedGoTargets(ctx context.Context, worktreeRoot string, changed []string) ([]Target, error) {
	// A missing worktree root is a hard error, not a fail-closed
	// TargetAll: checked here, before changedOwningPackages, so that
	// path's own subprocess failures (which legitimately mean "cannot
	// resolve this path") never mask a caller bug (a bad worktreeRoot)
	// behind a falsely reassuring TargetAll.
	if _, err := os.Stat(worktreeRoot); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "ci: worktree root %q is missing", worktreeRoot)
	}
	// Resolve the changed-path -> owning-package mapping BEFORE paying
	// for the full-tree graph subprocess: a go.mod/go.sum/vendor/deleted-
	// package change forces TargetAll regardless of the graph, so there
	// is nothing for the graph to compute against (D1 fail-closed fix).
	changedPkgs, ok := changedOwningPackages(ctx, worktreeRoot, changed)
	if !ok {
		return []Target{TargetAll}, nil
	}
	// changed is non-empty here (affected.go's dispatch already returns
	// []Target{} for an empty changed set before calling this function),
	// and every element of changed either resolved to an owning package
	// above or this function already returned via !ok -- changedPkgs is
	// therefore never empty at this point.
	graph, localPkgs, err := goListImportGraph(ctx, worktreeRoot)
	if err != nil {
		// D1 REWORK round 2 fix (confirm review case 3): worktreeRoot
		// itself was already proven to exist above (os.Stat), so an
		// error here can only be the go-list SUBPROCESS itself failing --
		// classically an unrelated, untouched package elsewhere in a
		// large monorepo that does not build (a stray cgo-less .c/.h
		// file, any other compile-breaking WIP), never this function's
		// own caller-supplied inputs. Fail CLOSED to TargetAll rather
		// than returning a raw error a caller might read as "nothing
		// affected" or abort on -- the same reasoning every other
		// "cannot compute" branch in this file already follows. The
		// underlying cause is recorded via slog (attention_subscribe.go's
		// own "warn, then degrade gracefully" precedent) as the reason,
		// not silently discarded.
		slog.Default().Warn("ci: whole-tree go list import graph failed; falling back to TargetAll",
			"worktree_root", worktreeRoot, "error", err)
		return []Target{TargetAll}, nil
	}
	localSet := make(map[string]bool, len(localPkgs))
	for _, p := range localPkgs {
		localSet[p] = true
	}
	var affected []string
	for _, pkg := range localPkgs {
		reachable := reachableLocal(graph, pkg, localSet)
		for cp := range changedPkgs {
			if reachable[cp] {
				affected = append(affected, pkg)
				break
			}
		}
	}
	sort.Strings(affected)
	targets := make([]Target, len(affected))
	for i, p := range affected {
		targets[i] = Target(p)
	}
	return targets, nil
}

// reachableLocal returns the set of local packages reachable from start
// by following graph's direct-import edges transitively (a BFS), pruned
// to localSet -- an external/std import is a dead end for this walk,
// never a local Target. start is always included: a package always
// "reaches" itself.
func reachableLocal(graph map[string][]string, start string, localSet map[string]bool) map[string]bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range graph[cur] {
			if !localSet[imp] || seen[imp] {
				continue
			}
			seen[imp] = true
			queue = append(queue, imp)
		}
	}
	return seen
}

// goListImportGraph runs the one real `go list` subprocess this path
// needs: worktreeRoot's full direct-import graph, plus the ordered
// vertex list (its local package import paths) for deterministic
// enumeration.
func goListImportGraph(ctx context.Context, worktreeRoot string) (map[string][]string, []string, error) {
	if _, err := os.Stat(worktreeRoot); err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindInvalidInput, err, "ci: worktree root %q is missing", worktreeRoot)
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-f", goListImportGraphTemplate, "./...")
	cmd.Dir = worktreeRoot
	cmd.Env = goListEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"ci: go list in %q: %s", worktreeRoot, strings.TrimSpace(stderr.String()))
	}
	graph, err := parseImportGraph(stdout.Bytes())
	if err != nil {
		return nil, nil, err
	}
	pkgs := make([]string, 0, len(graph))
	for pkg := range graph {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	return graph, pkgs, nil
}

// goListDirImportPath resolves relDir (a repository-relative directory)
// to its real import path via `go list -f {{.ImportPath}} ./<relDir>`.
// ok is false for any failure -- no buildable package there, an absent
// directory, or a go list error -- never surfaced as a hard error here:
// affected_go_mapping.go's resolveOwningDir/changedOwningPackages are the
// callers that decide what a false ok means (a testdata/ walk-up retry,
// or forcing TargetAll).
func goListDirImportPath(ctx context.Context, worktreeRoot, relDir string) (string, bool) {
	pattern := "./" + strings.TrimPrefix(path.Clean(relDir), "/")
	if relDir == "." || relDir == "" {
		pattern = "."
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.ImportPath}}", pattern)
	cmd.Dir = worktreeRoot
	cmd.Env = goListEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", false
	}
	pkg := strings.TrimSpace(stdout.String())
	if pkg == "" {
		return "", false
	}
	return pkg, true
}

// parseImportGraph parses goListImportGraphTemplate's real output into a
// package -> direct-imports map. A line that does not match the
// "<importpath>|<comma-separated-imports>" shape is a malformed-output
// error (fail-closed, 06 §5.20) rather than a silently dropped row --
// FuzzAffectedGoList (affected_fuzz_test.go) proves this never panics.
func parseImportGraph(data []byte) (map[string][]string, error) {
	graph := make(map[string][]string)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pkg, imports, err := parseImportGraphLine(line)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindIntegrity, err, "ci: malformed go list output at line %d", i+1)
		}
		graph[pkg] = imports
	}
	return graph, nil
}

// parseImportGraphLine parses one "<importpath>|<imports>" line.
func parseImportGraphLine(line string) (string, []string, error) {
	pkg, rest, found := strings.Cut(line, "|")
	if !found {
		return "", nil, cascade.New(cascade.KindIntegrity, "ci: line has no '|' separator")
	}
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return "", nil, cascade.New(cascade.KindIntegrity, "ci: empty import path")
	}
	rest = strings.TrimSpace(rest)
	var imports []string
	if rest != "" {
		for _, imp := range strings.Split(rest, ",") {
			if imp = strings.TrimSpace(imp); imp != "" {
				imports = append(imports, imp)
			}
		}
	}
	return pkg, imports, nil
}
