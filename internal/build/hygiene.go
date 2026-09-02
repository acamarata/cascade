// Package build (this file) implements the three static/structural halves
// of the Article-7 test-hygiene gate from P1-E01-W1-S01-T8
// (12-QUALITY-CONSTITUTION.md Art.7): the no-sleep-as-synchronization lint
// (Art.7.3), the no-network-unit-lane convention check (Art.7.2), and the
// pure assertion primitives the redirected-HOME + clean-tree CI step
// (Art.7.1) consumes. Running the ENTIRE suite under a redirected HOME is
// itself a CI STEP (ci.yml), not something this package does to itself —
// a nested `go test ./...` launched as a child of a `go test` process
// already running was probed directly (this ticket's coveragegate.go) and
// hangs on shared build-cache lock contention past a 5-minute timeout.
// Every live check in this file follows the same env-var-gated pattern
// coveragegate.go's TestCoverageGate_Live and commits.go/sweep.go's
// TestConventionalCommitGate_Live/TestIdentifierSweepGate_Live already
// establish: skipped locally, run for real only in the CI step that has
// already produced what it needs to assert against.
//
// # Art.7.2's honest scope
//
// "No network calls" in unit tests is not fully provable by static
// analysis: a legitimate unit test using net/http/httptest dials a
// LOCAL, in-process server and, depending on the exact call shape, may be
// indistinguishable from a real outbound call to an AST-only scanner. This
// gate enforces the narrower, PRECISELY provable half of Art.7.2 instead
// of a fragile approximation of the broader one: an untagged _test.go file
// (the DEFAULT unit lane every CI job runs without `-tags integration`)
// may not import "net" or "net/http" at all — real network I/O and
// httptest-based local-server tests alike require one of those imports, so
// forcing BOTH kinds behind the `integration` build tag is a real,
// enforceable rule, even though it is stricter than Art.7.2's literal
// text (an httptest-only test would need the tag too). This trade-off —
// simple and honest over precise and fragile — is recorded here
// deliberately, per this ticket's core lesson: a heuristic call-site
// matcher that tries to distinguish "http.Get(realURL)" from
// "http.Get(server.URL)" cannot be proven correct by inspection alone, and
// a wrong permissive heuristic is worse than an honest, slightly
// over-broad rule. Revisiting this if/when the repo gains its first
// httptest-based unit test is future work, named here rather than
// papered over. No file in the repo trips this today (verified by the
// real-tree test).
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// hygieneSleepAllowedPrefixes are module-relative directory prefixes where
// time.Sleep is the sanctioned synchronization primitive (Art.7.3's
// "allowlisted sync code" carve-out) — retry/backoff loops in the sync
// domain legitimately sleep between attempts. Empty today (internal/sync
// holds only a doc.go placeholder); the prefix is pre-declared so the
// first real sync ticket does not have to touch this gate to unblock
// itself.
var hygieneSleepAllowedPrefixes = []string{"internal/sync/"}

// hygieneSleepAllowedFiles is a narrow, individually-justified exemption
// list for pre-existing files this ticket found already using a
// documented, bounded time.Sleep OUTSIDE hygieneSleepAllowedPrefixes and
// outside this ticket's own files_scope (so this ticket cannot add a
// CASCADE-ALLOW escape comment to the file itself — that file belongs to a
// concurrently dispatched ticket per this ticket's HARD CONSTRAINTS).
// Probing this gate against the real tree found exactly one:
//
//   - internal/storage/storetest/queue_suite.go: pollForRedelivery() sleeps
//     a fixed time.Millisecond per iteration inside a hard-capped
//     maxPollAttempts loop; the function's own doc comment already argues
//     this stays deterministic under -shuffle and needs no injected
//     Clock. This reads as the legitimate case Art.7.3's carve-out exists
//     for (a bounded poll, not an unbounded synchronization sleep masking
//     a race), but the file's owner should adopt a
//     `// CASCADE-ALLOW: <ticket-id> <reason>` comment in-file once it is
//     next in scope, at which point this hardcoded entry should be
//     removed — recorded here rather than silently relaxing the gate for
//     everything, and rather than papering over a real, out-of-scope
//     finding.
var hygieneSleepAllowedFiles = map[string]bool{
	"internal/storage/storetest/queue_suite.go": true,
}

// HygieneSleepViolation is one denied time.Sleep call.
type HygieneSleepViolation struct {
	File string
	Line int
}

// hygieneIsSleepAllowed reports whether relPath (module-relative,
// forward-slash) falls under an allowlisted sync-code prefix or is one of
// the individually-justified file exemptions above.
func hygieneIsSleepAllowed(relPath string) bool {
	if hygieneSleepAllowedFiles[relPath] {
		return true
	}
	for _, prefix := range hygieneSleepAllowedPrefixes {
		if strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	return false
}

// NoSleepScanFile parses one Go source file and reports every time.Sleep
// call reached through the file's own "time" import (resolved through
// whatever local alias it declares — the same alias-resolution this
// package's clockgate.go and boundary_test.go already establish for
// exactly this reason: forbidigo has no rule for Sleep at all, so this
// gate is the ONLY line of enforcement and must not be text-matchable-
// evadable from day one).
func NoSleepScanFile(path string) ([]HygieneSleepViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	aliasToPkg := make(map[string]string)
	for _, imp := range file.Imports {
		importPath, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || importPath != "time" {
			continue
		}
		switch {
		case imp.Name == nil:
			aliasToPkg["time"] = "time"
		case imp.Name.Name == "_", imp.Name.Name == ".":
			// Blank import: nothing to call. Dot-import of "time" makes
			// Sleep(...) unqualified — out of THIS selector-based scan's
			// reach by construction, same documented limitation
			// boundary_test.go states for its own denied calls; "time"
			// dot-imported is vanishingly rare and not the evasion this
			// gate exists to close (that is the clock gate's job for
			// Now/Since, which DOES reject time dot-imports outright).
		default:
			aliasToPkg[imp.Name.Name] = "time"
		}
	}

	var out []HygieneSleepViolation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if aliasToPkg[pkgIdent.Name] != "time" || sel.Sel.Name != "Sleep" {
			return true
		}
		out = append(out, HygieneSleepViolation{File: path, Line: fset.Position(call.Pos()).Line})
		return true
	})
	return out, nil
}

// HygieneNetworkImportViolation is one untagged _test.go file importing
// "net" or "net/http" (Art.7.2's provable half — see this file's package
// doc).
type HygieneNetworkImportViolation struct {
	File   string
	Import string
}

// hygieneHasIntegrationTag reports whether src carries a
// "//go:build integration" (or the legacy "// +build integration")
// constraint anywhere in its build-constraint comment block.
func hygieneHasIntegrationTag(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "package ") {
			break
		}
		if strings.HasPrefix(trimmed, "//go:build") && strings.Contains(trimmed, "integration") {
			return true
		}
		if strings.HasPrefix(trimmed, "// +build") && strings.Contains(trimmed, "integration") {
			return true
		}
	}
	return false
}

// NoNetworkUnitTestScanFile checks one _test.go file: if it does not carry
// the integration build tag, it may not import "net" or "net/http".
func NoNetworkUnitTestScanFile(path string) ([]HygieneNetworkImportViolation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if hygieneHasIntegrationTag(data) {
		return nil, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []HygieneNetworkImportViolation
	for _, imp := range file.Imports {
		importPath, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		if importPath == "net" || importPath == "net/http" {
			out = append(out, HygieneNetworkImportViolation{File: path, Import: importPath})
		}
	}
	return out, nil
}

// HomeDirEntries returns the base names of every entry directly under dir
// (non-recursive is deliberate: even one stray top-level entry proves a
// test wrote somewhere under $HOME, which is exactly what Art.7.1 forbids
// — this function's job is to report what is there, not to judge which
// entry is "the important one"). A missing dir is reported as zero
// entries, never an error: a redirected-HOME dir that was never created is
// trivially untouched.
func HomeDirEntries(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// GitStatusPorcelain runs `git status --porcelain` in root and returns its
// trimmed output.
func GitStatusPorcelain(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// AssertGitStatusUnchanged is Art.7.1's actual "clean tree ... after the
// suite" assertion: NOT that git status is empty (a repo mid-development
// legitimately has uncommitted work; asserting emptiness would fail on
// every active ticket by construction), but that the test suite left the
// tree in EXACTLY the state it found it — a before/after snapshot
// comparison. Line order is not significant (a suite run cannot reorder
// git's own output deterministically across environments), so both
// snapshots are line-sorted before comparing.
func AssertGitStatusUnchanged(before, after string) (ok bool, diff string) {
	beforeLines := hygieneSortedLines(before)
	afterLines := hygieneSortedLines(after)

	beforeSet := make(map[string]bool, len(beforeLines))
	for _, l := range beforeLines {
		beforeSet[l] = true
	}
	var leaked []string
	for _, l := range afterLines {
		if !beforeSet[l] {
			leaked = append(leaked, l)
		}
	}
	if len(leaked) == 0 {
		return true, ""
	}
	return false, "leaked by the suite:\n" + strings.Join(leaked, "\n")
}

// hygieneSortedLines splits s into non-empty, trimmed, sorted lines.
func hygieneSortedLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	// Insertion sort is fine at this scale (a git status line count is
	// always small) and avoids importing "sort" for a two-caller helper.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
