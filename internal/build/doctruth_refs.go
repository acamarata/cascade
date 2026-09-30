package build

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

const doctruthDirective = "<!-- doctruth:illustrative -->"

var (
	pathPrefixes  = []string{"cmd/", "internal/", "pkg/", "providers/", "plugins/", "docs/", "apps/", ".github/"}
	lineTokenRe   = regexp.MustCompile(`^(.+):(\d+)(?:-(\d+))?$`)
	symbolTokenRe = regexp.MustCompile(`^((?:cmd|internal|pkg|providers|plugins)(?:/[\w-]+)*)\.([A-Z]\w*)(?:\.([A-Z]\w*))?$`)
	testTokenRe   = regexp.MustCompile(`^(Test|Fuzz|Benchmark|Example)[A-Za-z0-9_]*$`)
	wildcardChars = regexp.MustCompile(`[*?{,]`)
)

// refResolver holds the lazily-built indexes the symbol and test rules
// resolve against, and the tracked-file/dir sets the path and line rules
// resolve against.
type refResolver struct {
	repoRoot   string
	tracked    map[string]bool
	dirs       map[string]bool
	symbolDirs map[string]map[string]bool // dir -> {Ident, Type.Method}
	testNames  map[string]bool
	testsBuilt bool
}

func newRefResolver(repoRoot string, tracked, dirs map[string]bool) *refResolver {
	return &refResolver{repoRoot: repoRoot, tracked: tracked, dirs: dirs, symbolDirs: map[string]map[string]bool{}}
}

// checkRefs runs the path, line, symbol, test and directive rules over one
// page's non-fenced lines.
func (r *refResolver) checkRefs(page, content string, k *keyer) ([]DocFinding, int, error) {
	lines := parseDocLines(content)
	var findings []DocFinding
	refs := 0
	for i, dl := range lines {
		if dl.fenced {
			continue
		}
		lineNo := i + 1
		_, spans := stripInlineCode(dl.text)
		hasDirective := strings.Contains(dl.text, doctruthDirective)
		suppressed := false
		for _, tok := range spans {
			f, isRef, err := r.classify(tok)
			if err != nil {
				return nil, 0, err
			}
			if !isRef {
				continue
			}
			refs++
			if f == nil {
				continue
			}
			if hasDirective {
				suppressed = true
				continue
			}
			findings = append(findings, mkFinding(k, page, lineNo, f.Rule, f.Detail, dl.text))
		}
		if hasDirective && !suppressed {
			findings = append(findings, mkFinding(k, page, lineNo, DocRuleDirective,
				"doctruth:illustrative with no path/line/symbol/test finding on its line", dl.text))
		}
	}
	return findings, refs, nil
}

type refFinding struct {
	Rule   DocRule
	Detail string
}

// classify identifies which ref rule (if any) tok belongs to, returning
// (nil, true) when it matches a rule and resolves clean, (finding, true)
// when it matches and fails to resolve, and (nil, false) when tok is not a
// ref-shaped token at all.
func (r *refResolver) classify(tok string) (*refFinding, bool, error) {
	if strings.ContainsAny(tok, " \t<>") || tok == "" {
		// A bare citation token never contains whitespace, and '<'/'>'
		// mark a templated placeholder (e.g. providers/<vendor>/), never
		// a real citation.
		return nil, false, nil
	}
	callTok := strings.TrimSuffix(tok, "()")
	switch {
	case testTokenRe.MatchString(tok):
		ok, err := r.hasTestName(tok)
		if err != nil {
			return nil, true, err
		}
		if ok {
			return nil, true, nil
		}
		return &refFinding{DocRuleTest, "no test/fuzz/benchmark/example named " + tok}, true, nil
	case symbolTokenRe.MatchString(callTok):
		return r.classifySymbol(callTok)
	case lineTokenRe.MatchString(tok) && r.hasPathPrefix(strings.SplitN(tok, ":", 2)[0]):
		return r.classifyLine(tok)
	case r.hasPathPrefix(tok):
		if wildcardChars.MatchString(tok) {
			return nil, false, nil
		}
		clean := strings.TrimSuffix(tok, "/")
		if r.tracked[clean] || r.dirs[clean] {
			return nil, true, nil
		}
		return &refFinding{DocRulePath, "unresolved repo path: " + tok}, true, nil
	default:
		return nil, false, nil
	}
}

func (r *refResolver) hasPathPrefix(tok string) bool {
	for _, p := range pathPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

func (r *refResolver) classifyLine(tok string) (*refFinding, bool, error) {
	m := lineTokenRe.FindStringSubmatch(tok)
	filePath, n1s := m[1], m[2]
	if wildcardChars.MatchString(filePath) {
		return nil, false, nil
	}
	if !r.tracked[filePath] {
		return &refFinding{DocRuleLine, "unresolved path for line citation: " + filePath}, true, nil
	}
	data, err := readFileFn(pathJoin(r.repoRoot, filePath))
	if err != nil {
		return nil, true, wrapReadErr("doctruth: reading line-citation target", filePath, err)
	}
	total := strings.Count(string(data), "\n") + 1
	n1, _ := strconv.Atoi(n1s)
	n2 := n1
	if m[3] != "" {
		n2, _ = strconv.Atoi(m[3])
	}
	if n1 < 1 || n2 < n1 || n2 > total {
		return &refFinding{DocRuleLine, "line citation out of range: " + tok}, true, nil
	}
	return nil, true, nil
}

func (r *refResolver) classifySymbol(tok string) (*refFinding, bool, error) {
	m := symbolTokenRe.FindStringSubmatch(tok)
	dir, ident, method := m[1], m[2], m[3]
	names, err := r.symbolsIn(dir)
	if err != nil {
		return nil, true, err
	}
	if names == nil {
		return &refFinding{DocRuleSymbol, "unresolved package directory: " + dir}, true, nil
	}
	want := ident
	if method != "" {
		want = ident + "." + method
	}
	if names[want] {
		return nil, true, nil
	}
	return &refFinding{DocRuleSymbol, "unresolved symbol: " + tok}, true, nil
}

// symbolsIn returns the set of top-level identifiers and Type.Method pairs
// declared in dir's non-test .go files, or nil if dir has no tracked .go
// files at all (not itself an error: the caller reports it as a finding).
func (r *refResolver) symbolsIn(dir string) (map[string]bool, error) {
	if s, ok := r.symbolDirs[dir]; ok {
		return s, nil
	}
	names := map[string]bool{}
	found := false
	for f := range r.tracked {
		if filepath.ToSlash(filepath.Dir(f)) != dir {
			continue
		}
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		found = true
		data, err := readFileFn(pathJoin(r.repoRoot, f))
		if err != nil {
			return nil, wrapReadErr("doctruth: reading symbol source", f, err)
		}
		fset := token.NewFileSet()
		file, err := parseGoFileFn(fset, f, data)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindIntegrity, err, "doctruth: parsing %s", f)
		}
		collectDecls(file, names)
	}
	if !found {
		r.symbolDirs[dir] = nil
		return nil, nil
	}
	r.symbolDirs[dir] = names
	return names, nil
}

func collectDecls(file *ast.File, names map[string]bool) {
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil || len(decl.Recv.List) == 0 {
				names[decl.Name.Name] = true
				continue
			}
			names[recvTypeName(decl.Recv.List[0].Type)+"."+decl.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					names[ts.Name.Name] = true
				}
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range vs.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
}

func recvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// hasTestName reports whether tok is declared as a top-level func in any
// tracked _test.go file in the repo.
func (r *refResolver) hasTestName(tok string) (bool, error) {
	if !r.testsBuilt {
		if err := r.buildTestIndex(); err != nil {
			return false, err
		}
	}
	return r.testNames[tok], nil
}

func (r *refResolver) buildTestIndex() error {
	names := map[string]bool{}
	for f := range r.tracked {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := readFileFn(pathJoin(r.repoRoot, f))
		if err != nil {
			return wrapReadErr("doctruth: reading test source", f, err)
		}
		fset := token.NewFileSet()
		file, err := parseGoFileFn(fset, f, data)
		if err != nil {
			return cascade.Wrapf(cascade.KindIntegrity, err, "doctruth: parsing %s", f)
		}
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
				names[fn.Name.Name] = true
			}
		}
	}
	r.testNames = names
	r.testsBuilt = true
	return nil
}
