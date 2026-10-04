// Package build (this file) implements the static halves of the Article-7
// test-hygiene gate (quality constitution Art.7): the no-sleep-as-
// synchronization lint (Art.7.3), the no-network-unit-lane check (Art.7.2),
// and the pure assertions the redirected-HOME + clean-tree CI step (Art.7.1)
// consumes. Running the whole suite under a redirected HOME is a CI step, not
// something this package does to itself: a nested `go test ./...` under a
// running `go test` hangs on build-cache lock contention (probed for
// coveragegate.go). Live checks follow coveragegate.go's env-gated pattern:
// skipped locally, run for real only in the CI step.
//
// # Art.7.2's honest scope
//
// "No network calls" is not provable by static analysis: an httptest unit
// test dials a local server and can look exactly like a real outbound call.
// The gate enforces the provable, stricter half instead: an untagged _test.go
// file (the default lane) may not import "net" or "net/http" at all, so real
// network I/O and httptest servers alike sit behind the integration build
// tag. A call-site heuristic cannot be proven correct, and a wrong permissive
// one is worse than an honest, over-broad rule.
package build

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// hygieneSleepAllowedPrefixes are module-relative directory prefixes where
// time.Sleep is sanctioned (Art.7.3's "allowlisted sync code"): retry and
// backoff loops in the sync domain sleep between attempts. Pre-declared so
// the first real sync code does not have to touch this gate.
var hygieneSleepAllowedPrefixes = []string{"internal/sync/"}

// hygieneSleepAllowedFiles exempts individually justified files with a
// bounded time.Sleep outside hygieneSleepAllowedPrefixes. The one entry:
// storetest/queue_suite.go's pollForRedelivery sleeps 1ms per iteration of
// a hard-capped loop (a bounded poll, not a sleep masking a race); it should
// move to an in-file CASCADE-ALLOW comment and leave this list.
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
// call reached through the file's own "time" import, whatever alias it
// declares (as clockgate.go resolves them): forbidigo has no Sleep rule, so
// this gate is the only enforcement and must not be evadable by an alias.
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
			// Blank import: nothing to call. A dot-import makes Sleep
			// unqualified, out of this selector scan's reach (the limit
			// boundary_test.go states too); the clock gate rejects it.
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

// hygieneTooManyTags: a constraint naming more than 16 words is judged
// untagged (too many to enumerate, so the gate fails closed).
const hygieneTooManyTags = "too many build tags to enumerate"

// hygieneHasIntegrationTag reports whether src's build constraint keeps the
// file out of the default lane. It reads the header as go/build does, but
// stops at the first line that is not blank or a // comment (a //go:build in
// a /* */ block is no constraint); a //go:build line wins over +build lines,
// which count only above a blank line; two //go:build lines fail closed.
func hygieneHasIntegrationTag(src []byte) bool {
	var goBuild string
	var plus, pending []string
	for _, line := range strings.Split(string(src), "\n") {
		switch t := strings.TrimSpace(line); {
		case t == "":
			plus, pending = append(plus, pending...), nil
		case !strings.HasPrefix(t, "//"):
			return hygieneJudge(goBuild, plus)
		case constraint.IsGoBuild(t) && goBuild != "":
			return false
		case constraint.IsGoBuild(t):
			goBuild = t
		case constraint.IsPlusBuild(t):
			pending = append(pending, t)
		}
	}
	return hygieneJudge(goBuild, plus)
}

// hygieneJudge is true only when the header's constraint (the //go:build
// line, else the AND of the +build lines) is false for EVERY setting of its
// other tags while "integration" is unset.
func hygieneJudge(goBuild string, plus []string) bool {
	if goBuild != "" {
		plus = []string{goBuild}
	}
	var expr constraint.Expr
	for _, line := range plus {
		x, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		if expr != nil {
			x = &constraint.AndExpr{X: expr, Y: x}
		}
		expr = x
	}
	if expr == nil {
		return false
	}
	needs, _ := hygieneNeedsIntegration(expr, strings.Join(plus, " "))
	return needs
}

// hygieneNeedsIntegration enumerates every setting of the words on line
// (a superset of expr's tags; Eval short-circuits, so it cannot list them)
// with integration unset; any true evaluation builds the file in the
// default lane. The reason is empty when the file needs the tag.
func hygieneNeedsIntegration(expr constraint.Expr, line string) (bool, string) {
	words := strings.FieldsFunc(line, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.'
	})
	if len(words) > 16 {
		return false, hygieneTooManyTags
	}
	for mask := 0; mask < 1<<len(words); mask++ {
		if expr.Eval(func(tag string) bool {
			i := slices.Index(words, tag)
			return tag != "integration" && i >= 0 && mask&(1<<i) != 0
		}) {
			return false, "builds without the integration tag"
		}
	}
	return true, ""
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
// (non-recursive: one stray top-level entry already proves a test wrote
// under $HOME, which Art.7.1 forbids). A missing dir is zero entries, never
// an error: a redirected HOME that was never created is untouched.
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

// AssertGitStatusUnchanged is Art.7.1's "clean tree after the suite"
// assertion: not that git status is empty (work in progress is legitimate),
// but that the suite left the tree exactly as it found it. Line order is not
// significant, so both snapshots are line-sorted before comparing.
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
	slices.Sort(out)
	return out
}
