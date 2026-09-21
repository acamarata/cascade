package main

// Purpose (this file): the platform boundary the Windows build break ran
//   straight through — chat registration must COMPILE AND WORK on every
//   supported platform, while the daemon it normally runs under stays
//   absent on tier-2. UNTAGGED ON PURPOSE: a platform-tagged test could
//   not have caught F1.
//
// SPORT: cmd/cascade chat platform boundary (ADD) — P1-E45-W10-S88-T2.

import (
	"context"
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/rpc"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestP1ChatPlatformBoundary pins the three facts the repair rests on.
func TestP1ChatPlatformBoundary(t *testing.T) {
	t.Run("registration is portable", func(t *testing.T) {
		registry := chatRegistryForTest(t)
		for _, method := range []string{
			conversation.MethodAppendTurn,
			conversation.MethodGetThread,
			conversation.MethodListThreads,
		} {
			assertChatMethodBound(t, registry, method)
		}
	})

	t.Run("the daemon mode is not the embedded mode", func(t *testing.T) {
		// ModeEmbedded suppresses the SSE mirror, so registering under it
		// would drop the echo every chat client depends on.
		if chatDaemonMode == conversation.ModeEmbedded {
			t.Fatalf("chat registers under %q, the embedded mode; the SSE mirror would never fire", chatDaemonMode)
		}
	})
}

// assertChatMethodBound fails only on -32601: nothing answered at all.
func assertChatMethodBound(t *testing.T, registry *rpc.Registry, method string) {
	t.Helper()
	req, parseErr := rpc.Parse([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{}}`))
	if parseErr != nil {
		t.Fatalf("building a request for %s: %+v", method, parseErr)
	}
	_, errObj := registry.Dispatch(context.Background(), req)
	const codeMethodNotFound = -32601
	if errObj != nil && errObj.Code == codeMethodNotFound {
		t.Fatalf("%s is not registered on this platform: %+v", method, errObj)
	}
}

var supportedTuples = []struct{ GOOS, GOARCH string }{
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"windows", "amd64"},
}

// platformPackage is one platform's view: files kept, names declared, names referenced.
type platformPackage struct {
	goos, goarch string
	files        []string
	decls        map[string]string // name -> declaring file
	refs         map[string]string // name -> a file referencing it
}

// TestP1ChatPlatformCompilation is audit F1 turned into a gate: F1 was a
// SYMBOL TABLE bug, so this asserts the closure property on every tuple CI
// builds — every package-level name REFERENCED in the file set go keeps
// for a platform is also DECLARED in it, using go/build's own evaluator.
func TestP1ChatPlatformCompilation(t *testing.T) {
	pkgs := loadPlatformPackages(t)

	union := map[string]bool{}
	for _, p := range pkgs {
		for name := range p.decls {
			union[name] = true
		}
	}

	checked := 0
	for _, p := range pkgs {
		if _, ok := p.decls["wireChatHandlers"]; !ok {
			t.Errorf("%s/%s does not declare wireChatHandlers; audit F1 is back", p.goos, p.goarch)
		}
		for name, ref := range p.refs {
			if !union[name] {
				continue
			}
			checked++
			if _, ok := p.decls[name]; !ok {
				t.Errorf("%s/%s: %s references %s, declared only on other platforms; `GOOS=%s go build ./cmd/cascade` fails with `undefined: %s`",
					p.goos, p.goarch, ref, name, p.goos, name)
			}
		}
	}

	// A closure check over an empty set is green and means nothing.
	if checked == 0 {
		t.Fatal("no package-level calls were checked; the gate resolved nothing and cannot fail")
	}
	t.Logf("checked %d package-level references across %d platform tuples", checked, len(pkgs))
}

// TestP1WindowsChatRefusal pins the OTHER half: portable chat wiring must
// not have given tier-2 a daemon. It runs the portable refusal seam.
func TestP1WindowsChatRefusal(t *testing.T) {
	pkgs := loadPlatformPackages(t)
	byGOOS := map[string]*platformPackage{}
	for i := range pkgs {
		byGOOS[pkgs[i].goos] = &pkgs[i]
	}
	windows, darwin := byGOOS["windows"], byGOOS["darwin"]
	if windows == nil || darwin == nil {
		t.Fatal("the platform matrix no longer covers both windows and darwin")
	}
	t.Run("the refusal is typed, not a panic and not a silent success", assertTypedWindowsRefusal)
	t.Run("every daemon verb keeps a windows counterpart", func(t *testing.T) {
		assertWindowsDaemonVerbs(t, darwin, windows)
	})
	t.Run("the one-shot chat surface survives the refusal", func(t *testing.T) {
		assertWindowsOneShotChat(t, windows)
	})
}

// assertTypedWindowsRefusal executes the portable refusal itself.
func assertTypedWindowsRefusal(t *testing.T) {
	err := resume.RefuseOnGOOS("windows")
	if err == nil {
		t.Fatal("windows resume returned nil; tier-2 would pretend to resume a daemon it has never had")
	}
	if !errors.Is(err, resume.ErrWindowsUnsupported) {
		t.Fatalf("windows resume error = %v, want ErrWindowsUnsupported", err)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("windows resume error = %v, want KindUnsupported", err)
	}
	if err := resume.RefuseOnGOOS("darwin"); err != nil {
		t.Fatalf("darwin resume error = %v, want nil; a tier-1 platform lost its daemon", err)
	}
}

// assertWindowsDaemonVerbs checks every darwin verb has a windows one.
func assertWindowsDaemonVerbs(t *testing.T, darwin, windows *platformPackage) {
	t.Helper()
	verbs := 0
	for name, file := range darwin.decls {
		if !strings.HasPrefix(name, "platformDaemon") {
			continue
		}
		verbs++
		declFile, ok := windows.decls[name]
		if !ok {
			t.Errorf("%s is declared in %s with no windows declaration; `cascade daemon` would not compile on tier-2, or would not refuse", name, file)
			continue
		}
		if !strings.Contains(declFile, "windows") {
			t.Errorf("windows takes %s from %s, not a windows-specific file; a unix lifecycle leaked onto tier-2", name, declFile)
		}
	}
	if verbs == 0 {
		t.Fatal("no platformDaemon* verbs were found; the gate resolved nothing and cannot fail")
	}
	t.Logf("%d daemon verbs each keep a windows refusal", verbs)
}

// assertWindowsOneShotChat is "one-shot chat, no daemon" as code.
func assertWindowsOneShotChat(t *testing.T, windows *platformPackage) {
	t.Helper()
	if _, ok := windows.decls["wireChatHandlers"]; !ok {
		t.Error("windows does not declare wireChatHandlers; the one-shot chat path was dropped to fix the build")
	}
	if cascadeinit.DaemonSupported("windows") {
		t.Error("windows reports daemon support; the repair exposed a daemon tier-2 does not have")
	}
	if !cascadeinit.DaemonSupported("linux") || !cascadeinit.DaemonSupported("darwin") {
		t.Error("a tier-1 platform lost daemon support")
	}
	if !slices.Contains(windows.files, "builtin_plugins.go") {
		t.Error("windows does not build builtin_plugins.go; `cascade chat` would not be mounted at all")
	}
}

// loadPlatformPackages resolves this package once per supported tuple.
func loadPlatformPackages(t *testing.T) []platformPackage {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the package directory: %v", err)
	}
	out := make([]platformPackage, 0, len(supportedTuples))
	for _, tuple := range supportedTuples {
		ctxt := build.Default
		ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled = tuple.GOOS, tuple.GOARCH, false
		pkg, importErr := ctxt.ImportDir(dir, 0)
		if importErr != nil {
			t.Fatalf("resolving this package for %s/%s: %v", tuple.GOOS, tuple.GOARCH, importErr)
		}
		if len(pkg.GoFiles) == 0 {
			t.Fatalf("%s/%s keeps no files from this package", tuple.GOOS, tuple.GOARCH)
		}
		p := platformPackage{
			goos: tuple.GOOS, goarch: tuple.GOARCH, files: pkg.GoFiles,
			decls: map[string]string{}, refs: map[string]string{},
		}
		fset := token.NewFileSet()
		for _, base := range pkg.GoFiles {
			file, parseErr := parser.ParseFile(fset, filepath.Join(dir, base), nil, 0)
			if parseErr != nil {
				t.Fatalf("parsing %s: %v", base, parseErr)
			}
			collectPackageDecls(file, base, p.decls)
			collectPackageRefs(file, base, p.refs)
		}
		out = append(out, p)
	}
	return out
}

// collectPackageDecls records every package-level name file declares.
func collectPackageDecls(file *ast.File, base string, into map[string]string) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				into[d.Name.Name] = base
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					into[s.Name.Name] = base
				case *ast.ValueSpec:
					for _, name := range s.Names {
						into[name.Name] = base
					}
				}
			}
		}
	}
}

// collectPackageRefs records the two unqualified-identifier positions a
// build tag can break: `name(...)` and `Name{...}`.
func collectPackageRefs(file *ast.File, base string, into map[string]string) {
	ast.Inspect(file, func(n ast.Node) bool {
		var name string
		switch x := n.(type) {
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				name = id.Name
			}
		case *ast.CompositeLit:
			if id, ok := x.Type.(*ast.Ident); ok {
				name = id.Name
			}
		}
		if _, seen := into[name]; name != "" && !seen {
			into[name] = base
		}
		return true
	})
}
