// Package build (this file): the private-planning gate (Constitution C6,
// Decision 9). Product code, tests included, must never read the private
// planning trees: no string literal and no filepath.Join argument run may
// name .claude/{planning,project,evidence,reforge,phases,temp,worktrees}.
// Product use of .claude itself (harness projection, hygiene patterns,
// .claude/hygiene) stays legal; only the seven private subtrees are gated.
//
// The scan is AST based: string literals, constant "+" concatenation of
// literals, and runs of adjacent literal arguments of a Join call. Comments
// never trigger. There is no exemption list or allow file.
//
// Known limits (by design, not gaps to be waved through): a path built
// from non-constant parts (fmt.Sprintf, a variable segment, a computed
// concatenation) is invisible to a syntax scan. Other known limits: const
// identifier segments (including a const split across declarations),
// strings.Join([]string{...}, "/"), spread arguments to Join, an aliased or
// dot-imported Join, //go:embed directives (comments), and
// os.DirFS(".claude") (bare .claude is legal). Product code with a
// legitimate need for a planning root takes it as an explicit input
// instead (see internal/coverage).
package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pplForbidden matches a private planning subtree reference inside any
// string: ".claude/<segment>" where the segment ends at a separator or the
// end of the text (so ".claude/projects" and ".claude/hygiene" stay legal).
var pplForbidden = regexp.MustCompile(`\.claude/(planning|project|evidence|reforge|phases|temp|worktrees)([^A-Za-z0-9]|$)`)

// pplMatches reports whether text names a private planning subtree. The
// text is normalised first: backslashes become slashes, the path is cleaned
// (so ".claude/", ".claude//x", ".claude/./x" and a Join run with a
// leading or trailing slash segment collapse) and lower-cased (the macOS
// filesystem is case-insensitive).
func pplMatches(text string) bool {
	norm := path.Clean(strings.ReplaceAll(text, `\`, "/"))
	return pplForbidden.MatchString(strings.ToLower(norm))
}

// pplFold evaluates a string literal, a parenthesised one, or a constant
// "+" concatenation of those. ok is false for anything non-constant.
func pplFold(e ast.Expr) (string, bool) {
	switch n := e.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(n.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return pplFold(n.X)
	case *ast.BinaryExpr:
		if n.Op != token.ADD {
			return "", false
		}
		l, lok := pplFold(n.X)
		r, rok := pplFold(n.Y)
		return l + r, lok && rok
	}
	return "", false
}

// pplJoinRuns returns, for a call whose callee is named Join, each run of
// adjacent constant arguments joined with "/". A non-constant argument
// ends a run: it could be anything.
func pplJoinRuns(call *ast.CallExpr) []string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return nil
	}
	var runs, cur []string
	flush := func() {
		if len(cur) > 1 {
			runs = append(runs, strings.Join(cur, "/"))
		}
		cur = nil
	}
	for _, a := range call.Args {
		if s, ok := pplFold(a); ok {
			cur = append(cur, s)
			continue
		}
		flush()
	}
	flush()
	return runs
}

// pplScanSource parses src (named name for positions) and returns one
// "name:line: text" entry per private planning reference.
func pplScanSource(name string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	hit := func(p token.Pos, text string) {
		out = append(out, fmt.Sprintf("%s:%d: %s", name, fset.Position(p).Line, text))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			for _, run := range pplJoinRuns(call) {
				if pplMatches(run) {
					hit(call.Pos(), run)
				}
			}
			return true
		}
		expr, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		s, folded := pplFold(expr)
		if !folded {
			return true
		}
		if pplMatches(s) {
			hit(n.Pos(), s)
		}
		return false
	})
	return out, nil
}

// pplScanTree walks every .go file under root (tests included), skipping
// testdata/ and dot directories, and returns the sorted hits with
// root-relative, slash-separated paths.
func pplScanTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && archIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // repo walk, read-only
		if err != nil {
			return err
		}
		hits, err := pplScanSource(filepath.ToSlash(rel), data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", rel, err)
		}
		out = append(out, hits...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("private-planning gate: walking %s: %v", root, walkErr)
	}
	sort.Strings(out)
	return out
}

// TestNoProductCodeReadsPrivatePlanning is the live gate: no tracked .go
// file names a private planning subtree.
func TestNoProductCodeReadsPrivatePlanning(t *testing.T) {
	hits := pplScanTree(t, archModuleRoot(t))
	if len(hits) != 0 {
		t.Fatalf("product code names %d private planning path(s) (Constitution C6):\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// pplFixtureDir is the seeded-violation fixture tree.
func pplFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(archModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "private-planning")
}

// TestNoProductCodeReadsPrivatePlanning_Seeded proves the gate fires: the
// Join-segment and concatenation fixtures are found by file name, and the
// clean fixture (product-legal .claude use, a comment, a variable segment)
// is not.
func TestNoProductCodeReadsPrivatePlanning_Seeded(t *testing.T) {
	hits := pplScanTree(t, pplFixtureDir(t))
	joined := strings.Join(hits, "\n")
	t.Logf("seeded fixture hits:\n%s", joined)
	for _, want := range []string{"join_violation.go:", "concat_violation.go:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("seeded fixture %s not found by the gate; hits:\n%s", want, joined)
		}
	}
	if len(hits) != 2 {
		t.Errorf("got %d hit(s), want exactly 2 (clean.go must not fire):\n%s", len(hits), joined)
	}
	if strings.Contains(joined, "clean.go") {
		t.Errorf("clean fixture tripped the gate:\n%s", joined)
	}
}

// pplSrc wraps one Go expression in a minimal file. The test builds the
// expressions from segments so this file itself holds no forbidden literal.
func pplSrc(expr string) string {
	return "package p\nvar _ = " + expr + "\n"
}

// TestPrivatePlanningScan_Shapes pins the scanner's behaviour on each
// source shape, including the literal forms the fixtures cannot hold.
func TestPrivatePlanningScan_Shapes(t *testing.T) {
	c := "." + "claude"
	cases := []struct {
		name string
		expr string
		want int
	}{
		{"literal", `"` + c + `/planning/p1"`, 1},
		{"raw literal", "`" + c + "/evidence`", 1},
		{"backslash literal", `"` + c + `\\worktrees\\x"`, 1},
		{"literal inside text", `"reading ` + c + `/reforge/x failed"`, 1},
		{"concatenation", `"` + c + `/" + "phases"`, 1},
		{"join segments", `filepath.Join(r, "` + c + `", "temp")`, 1},
		{"join trailing slash segment", `filepath.Join(r, "` + c + `/", "planning")`, 1},
		{"join leading slash segment", `filepath.Join(r, "` + c + `", "/planning")`, 1},
		{"double slash", `"` + c + `//planning"`, 1},
		{"dot segment", `"` + c + `/./planning"`, 1},
		{"mixed case", `"` + c + `/Planning"`, 1},
		{"join trailing slash temp", `filepath.Join(r, "` + c + `/", "temp")`, 1},
		{"join one literal", `filepath.Join(r, "` + c + `/project")`, 1},
		{"hygiene is legal", `"` + c + `/hygiene"`, 0},
		{"projects is legal", `"` + c + `/projects/x"`, 0},
		{"bare claude is legal", `filepath.Join(r, "` + c + `", "CLAUDE.md")`, 0},
		{"variable segment is a known limit", `filepath.Join(r, "` + c + `", seg)`, 0},
		{"sprintf is a known limit", `fmt.Sprintf("` + c + `/%s", "planning")`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := pplScanSource("x.go", pplSrc(tc.expr))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(hits) != tc.want {
				t.Fatalf("hits = %v, want %d", hits, tc.want)
			}
		})
	}
}

// TestPrivatePlanningScan_CommentsNeverTrigger proves a comment naming a
// private tree is ignored (the gate reads the AST, not the text).
func TestPrivatePlanningScan_CommentsNeverTrigger(t *testing.T) {
	c := "." + "claude"
	src := "package p\n// see " + c + "/planning/p1 for the plan\nvar _ = 1\n"
	hits, err := pplScanSource("x.go", src)
	if err != nil || len(hits) != 0 {
		t.Fatalf("comment tripped the gate: hits=%v err=%v", hits, err)
	}
}
