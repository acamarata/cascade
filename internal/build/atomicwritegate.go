// Package build (this file) implements the atomic-write gate of
// contract:atomic-file-write (P1-CORE-08, R8 F-1).
//
// No non-test Go file under cmd/ or internal/ may reference os.WriteFile or
// ioutil.WriteFile: both truncate before they write, so a crash mid-write
// leaves an empty or torn file. State goes through internal/runtime's
// WriteFileAtomic family. The only exceptions are rows of the OWNED list
// testdata/atomicwrite-exemptions.tsv (file, enclosing symbol, exact
// reference count, owning PEWTT ticket, reason).
//
// The gate fails on: an unlisted reference; a stale row (so an owner must
// delete its row when it removes its write); a count mismatch; a bad owner or empty reason; a missing
// list, wrong header, wrong column count or duplicate row; a dot-import of
// os or io/ioutil; and a walk that parsed fewer files than its floor or
// missed a required file. Imports are resolved per file (aliases caught)
// and the selector matches anywhere, so a function value counts too, as in
// clockgate.go and outputgate.go. Not seen, by construction: a WriteFile
// behind another package's wrapper (pkg/ and plugins/ are outside, C3) and
// other truncating opens (os.Create, O_TRUNC), which are not gated (R8
// F-21, docs/storage.md). There is no escape comment.
package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// atomicWriteHeader is the exemption list's exact first line.
const atomicWriteHeader = "file\tsymbol\tcount\towner\treason"

// atomicWriteOwnerPattern is a PEWTT ticket id: never UNOWNED, never free text.
var atomicWriteOwnerPattern = regexp.MustCompile(`^P\d+-[A-Z]{2,5}-\d{2,3}$`)

// atomicWriteTracked maps the import paths whose WriteFile is denied.
var atomicWriteTracked = map[string]string{"os": "os", "io/ioutil": "ioutil"}

// atomicWriteRoots are the module-relative trees the gate scans.
var atomicWriteRoots = []string{"cmd", "internal"}

// atomicWriteHit is one WriteFile reference (or dot-import) found by AST;
// File is module-relative slash form, Symbol the enclosing func.
type atomicWriteHit struct {
	File, Symbol, Call string
	Line               int
}

// atomicWriteRow is one parsed exemption row.
type atomicWriteRow struct {
	File, Symbol, Owner, Reason string
	Count                       int
}

// atomicWriteConfig is one run: tree root, list, and walk-integrity floor.
type atomicWriteConfig struct {
	Root     string
	TSV      string
	MinFiles int
	Required []string
}

// atomicWriteScan is what one walk of the roots found.
type atomicWriteScan struct {
	files   int
	visited map[string]bool
	hits    []atomicWriteHit
}

// checkAtomicWrites runs the gate and returns every problem; a failure to
// read or parse anything is itself a problem, never a silent pass.
func checkAtomicWrites(cfg atomicWriteConfig) []string {
	var problems []string
	rows, rowProblems := loadAtomicWriteRows(cfg.TSV)
	problems = append(problems, rowProblems...)
	scan, err := scanAtomicWriteTree(cfg.Root)
	if err != nil {
		return append(problems, "scan: "+err.Error())
	}
	if scan.files < cfg.MinFiles {
		problems = append(problems, fmt.Sprintf("scan parsed %d non-test Go files, fewer than %d: the walk is broken", scan.files, cfg.MinFiles))
	}
	for _, rel := range cfg.Required {
		if !scan.visited[rel] {
			problems = append(problems, "scan never visited "+rel)
		}
	}
	return append(problems, matchAtomicWriteRows(scan.hits, rows)...)
}

// matchAtomicWriteRows compares the references found with the rows.
func matchAtomicWriteRows(hits []atomicWriteHit, rows []atomicWriteRow) []string {
	var problems []string
	found := map[[2]string][]atomicWriteHit{}
	for _, h := range hits {
		if strings.HasPrefix(h.Call, "dot-import:") {
			problems = append(problems, fmt.Sprintf("%s:%d: %s cannot be gated; import it by name", h.File, h.Line, h.Call))
			continue
		}
		key := [2]string{h.File, h.Symbol}
		found[key] = append(found[key], h)
	}
	listed := map[[2]string]bool{}
	for _, r := range rows {
		key := [2]string{r.File, r.Symbol}
		listed[key] = true
		n := len(found[key])
		switch {
		case n == 0:
			problems = append(problems, fmt.Sprintf("stale row %s %s: no WriteFile left there; delete the row", r.File, r.Symbol))
		case n != r.Count:
			problems = append(problems, fmt.Sprintf("count mismatch for %s %s: row says %d, found %d", r.File, r.Symbol, r.Count, n))
		}
	}
	for key, hs := range found {
		if listed[key] {
			continue
		}
		for _, h := range hs {
			problems = append(problems, fmt.Sprintf("unlisted bare write %s:%d in %s: %s; use runtime.WriteFileAtomic", h.File, h.Line, h.Symbol, h.Call))
		}
	}
	sort.Strings(problems)
	return problems
}

// loadAtomicWriteRows parses the list, reporting every malformed line.
func loadAtomicWriteRows(path string) ([]atomicWriteRow, []string) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the gate's own fixture or tracked list
	if err != nil {
		return nil, []string{"exemption list missing or unreadable: " + err.Error()}
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if lines[0] != atomicWriteHeader {
		return nil, []string{fmt.Sprintf("exemption list header is %q, want %q", lines[0], atomicWriteHeader)}
	}
	var rows []atomicWriteRow
	var problems []string
	seen := map[[2]string]bool{}
	for i, line := range lines[1:] {
		r, err := parseAtomicWriteRow(line)
		if err != nil {
			problems = append(problems, fmt.Sprintf("exemption list line %d: %v", i+2, err))
			continue
		}
		key := [2]string{r.File, r.Symbol}
		if seen[key] {
			problems = append(problems, fmt.Sprintf("exemption list line %d: duplicate row %s %s", i+2, r.File, r.Symbol))
			continue
		}
		seen[key] = true
		rows = append(rows, r)
	}
	return rows, problems
}

// parseAtomicWriteRow validates one tab-separated row.
func parseAtomicWriteRow(line string) (atomicWriteRow, error) {
	cols := strings.Split(line, "\t")
	if len(cols) != 5 {
		return atomicWriteRow{}, fmt.Errorf("want 5 tab-separated columns, got %d", len(cols))
	}
	r := atomicWriteRow{File: cols[0], Symbol: cols[1], Owner: cols[3], Reason: strings.TrimSpace(cols[4])}
	count, err := strconv.Atoi(cols[2])
	if err != nil || count < 1 {
		return atomicWriteRow{}, fmt.Errorf("count %q is not a positive integer", cols[2])
	}
	r.Count = count
	switch {
	case r.File == "" || r.Symbol == "":
		return atomicWriteRow{}, fmt.Errorf("empty file or symbol")
	case !atomicWriteOwnerPattern.MatchString(r.Owner):
		return atomicWriteRow{}, fmt.Errorf("owner %q is not a PEWTT ticket id", r.Owner)
	case r.Reason == "":
		return atomicWriteRow{}, fmt.Errorf("empty reason")
	}
	return r, nil
}

// scanAtomicWriteTree walks the roots for non-test Go files (no testdata/).
func scanAtomicWriteTree(root string) (atomicWriteScan, error) {
	scan := atomicWriteScan{visited: map[string]bool{}}
	for _, top := range atomicWriteRoots {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			hits, err := scanAtomicWriteFile(path, rel)
			if err != nil {
				return err
			}
			scan.files++
			scan.visited[rel] = true
			scan.hits = append(scan.hits, hits...)
			return nil
		})
		if err != nil {
			return scan, err
		}
	}
	return scan, nil
}

// scanAtomicWriteFile reports one file's WriteFile references and dot-imports.
func scanAtomicWriteFile(path, rel string) ([]atomicWriteHit, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	var hits []atomicWriteHit
	for _, imp := range file.Imports {
		ip, _ := strconv.Unquote(imp.Path.Value)
		local, tracked := atomicWriteTracked[ip]
		switch {
		case !tracked:
		case imp.Name == nil:
			aliases[local] = local
		case imp.Name.Name == ".":
			hits = append(hits, atomicWriteHit{File: rel, Symbol: "(file)", Line: fset.Position(imp.Pos()).Line, Call: "dot-import:" + ip})
		case imp.Name.Name != "_":
			aliases[imp.Name.Name] = local
		}
	}
	for _, decl := range file.Decls {
		hits = append(hits, atomicWriteDeclHits(fset, rel, decl, aliases)...)
	}
	return hits, nil
}

// atomicWriteDeclHits reports the WriteFile references inside one decl.
func atomicWriteDeclHits(fset *token.FileSet, rel string, decl ast.Decl, aliases map[string]string) []atomicWriteHit {
	symbol := atomicWriteSymbol(decl)
	var hits []atomicWriteHit
	ast.Inspect(decl, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WriteFile" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] != "" {
			hits = append(hits, atomicWriteHit{File: rel, Symbol: symbol, Line: fset.Position(sel.Pos()).Line,
				Call: aliases[id.Name] + ".WriteFile"})
		}
		return true
	})
	return hits
}

// atomicWriteSymbol names a decl as rows do: Func, Type.Method or (package).
func atomicWriteSymbol(decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "(package)"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	expr := fn.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name + "." + fn.Name.Name
		default:
			return "?." + fn.Name.Name
		}
	}
}
