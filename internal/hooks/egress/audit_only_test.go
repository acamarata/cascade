package egress

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestAuditOnlyMatchesTransit(t *testing.T) {
	want := []EgressClass{EgressClassOAuth, EgressClassSpikeMeasurement, EgressClassWikiGitPush}
	if got := AuditOnlyClasses(); !reflect.DeepEqual(got, want) {
		t.Errorf("audit-only=%v, want %v", got, want)
	}
	root := auditModuleRoot(t)
	files := auditSourceFiles(t, root)
	transited := auditTransitClasses(files)
	if len(transited) == 0 {
		t.Fatal("empty transit inventory")
	}
	hookOK := auditHookProof(t, root)
	for _, class := range DefaultRegistry().Classes() {
		cfg, ok := DefaultRegistry().Lookup(class)
		if !ok {
			t.Fatalf("missing class %s", class)
		}
		reason, audit := AuditOnly(class)
		if audit && (reason == "" || transited[class]) {
			t.Errorf("audit-only %s: reason=%q transit=%v", class, reason, transited[class])
		}
		if cfg.Enabled && !audit && !transited[class] && (class != EgressClassHook || !hookOK) {
			t.Errorf("enabled %s has no transit proof", class)
		}
	}
	if !hookOK {
		t.Error("hook token guard or Intercept token argument missing")
	}
	if reason, ok := AuditOnly(EgressClass("unknown")); ok || reason != "" {
		t.Fatal("unknown class marked audit-only")
	}
	copyOf := AuditOnlyClasses()
	if len(copyOf) > 0 {
		copyOf[0] = "changed"
	}
	if !reflect.DeepEqual(AuditOnlyClasses(), want) {
		t.Error("inventory aliases mutable state")
	}
}

func auditModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}

func auditSourceFiles(t *testing.T, root string) map[string]*ast.File {
	t.Helper()
	files := map[string]*ast.File{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !auditBuildable(path, src) {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err == nil {
			files[path] = f
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no source files parsed")
	}
	return files
}

// auditBuildable reports whether path compiles into a shipped binary: some
// darwin, linux or windows build on amd64 or arm64, with or without cgo, no
// custom tags, and this toolchain's release tags. go/build applies both
// //go:build lines and GOOS/GOARCH filename suffixes. A file excluded
// everywhere (for example //go:build ignore or x_plan9.go) proves nothing.
func auditBuildable(path string, src []byte) bool {
	dir, name := filepath.Split(path)
	open := func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(src)), nil }
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			for _, cgo := range []bool{false, true} {
				ctxt := build.Default
				ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled, ctxt.BuildTags, ctxt.OpenFile = goos, goarch, cgo, nil, open
				if ok, err := ctxt.MatchFile(dir, name); err == nil && ok {
					return true
				}
			}
		}
	}
	return false
}

func auditTransitClasses(files map[string]*ast.File) map[EgressClass]bool {
	constants := map[string]ast.Expr{}
	for path, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, name := range v.Names {
					if i < len(v.Values) {
						constants[filepath.Dir(path)+"/"+name.Name] = v.Values[i]
					}
				}
			}
		}
	}
	out := map[EgressClass]bool{}
	for path, f := range files {
		if filepath.Base(path) == "classes.go" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Capability" && sel.Sel.Name != "Intercept" && sel.Sel.Name != "InterceptClass") {
				return true
			}
			for _, arg := range call.Args {
				if value := auditClassValue(arg, path, f, files, constants, 0); value != "" {
					out[EgressClass(value)] = true
				}
			}
			return true
		})
	}
	return out
}

func auditClassValue(e ast.Expr, path string, f *ast.File, files map[string]*ast.File, constants map[string]ast.Expr, depth int) string {
	if depth > 10 {
		return ""
	}
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			value, _ := strconv.Unquote(v.Value)
			return value
		}
	case *ast.Ident:
		if next := constants[filepath.Dir(path)+"/"+v.Name]; next != nil {
			return auditClassValue(next, path, f, files, constants, depth+1)
		}
	case *ast.SelectorExpr:
		id, ok := v.X.(*ast.Ident)
		if !ok {
			return ""
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name != id.Name || !strings.HasPrefix(p, "github.com/acamarata/cascade/") {
				continue
			}
			for target, source := range files {
				if strings.HasSuffix(filepath.ToSlash(filepath.Dir(target)), "/"+strings.TrimPrefix(p, "github.com/acamarata/cascade/")) {
					return auditClassValue(ast.NewIdent(v.Sel.Name), target, source, files, constants, depth+1)
				}
			}
		}
	}
	return ""
}

func TestAuditBuildableSkipsExcludedFiles(t *testing.T) {
	dir := "pkg"
	cases := map[string]struct {
		header string
		want   bool
	}{
		"plain.go":       {"", true},
		"notwin.go":      {"//go:build !windows\n\n", true},
		"win.go":         {"//go:build windows\n\n", true},
		"cgo.go":         {"//go:build darwin && cgo\n\n", true},
		"ignored.go":     {"//go:build ignore\n\n", false},
		"postgres.go":    {"//go:build postgres\n\n", false},
		"wasi.go":        {"//go:build wasip1\n\n", false},
		"future.go":      {"//go:build go1.999\n\n", false},
		"x_plan9.go":     {"", false},
		"x_linux_386.go": {"", false},
		"x_windows.go":   {"", true},
	}
	for name, c := range cases {
		if got := auditBuildable(filepath.Join(dir, name), []byte(c.header+"package x\n")); got != c.want {
			t.Errorf("auditBuildable(%s)=%v, want %v", name, got, c.want)
		}
	}
}

// TestStoredValueIn covers the strict URL match: no tag exemption, the
// caller's floor, optional case folding, and the capability checked first.
func TestStoredValueIn(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t, map[string][]byte{"short": []byte("Zq4x"), "empty": nil, "long": []byte("fakepassword9")})
	token := mustCapability(t, engine, EgressClassRegistryFetch)
	for _, c := range []struct {
		text      string
		min       int
		fold, hit bool
	}{
		{"/a/Zq4x/b", 4, false, true},
		{"/a/zq4x/b", 4, false, false},
		{"zq4x.example", 4, true, true},
		{"/a/Zq4x/b", 5, false, false},
		{"<apikey>fakepassword9</apikey>", 4, false, true},
		{"/plain/path", 0, false, false},
	} {
		got, err := engine.StoredValueIn(ctx, token, c.text, c.min, c.fold)
		if err != nil || got != c.hit {
			t.Errorf("StoredValueIn(%q, %d, %v) = %v, %v; want %v", c.text, c.min, c.fold, got, err, c.hit)
		}
	}
	if _, err := engine.StoredValueIn(ctx, Capability{}, "Zq4x", 4, false); !errors.Is(err, ErrCapabilityRequired) {
		t.Errorf("zero capability error = %v, want ErrCapabilityRequired", err)
	}
	failing := errors.New("vault read failed")
	for name, vault := range map[string]*mapVault{
		"list": {listErr: failing},
		"get":  {values: map[string][]byte{"v": []byte("Zq4x")}, getErr: failing},
	} {
		e := newEngineOn(t, DefaultRegistry(), vault)
		if got, err := e.StoredValueIn(ctx, mustCapability(t, e, EgressClassRegistryFetch), "Zq4x", 4, false); got || !errors.Is(err, failing) {
			t.Errorf("%s failure: got=%v error=%v, want the vault error", name, got, err)
		}
	}
}
