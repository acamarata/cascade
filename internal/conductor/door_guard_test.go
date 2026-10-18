package conductor

// Purpose (this file): TestDoorIsOnlyDispatchPath - the go/ast guard that no
//   code can reach a provider except through the door's own dispatch helpers,
//   and that each door calls enterDoor first. The real tree must be clean and
//   every bypass shape, seeded as a fixture, must be red.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// doorDispatchFns are the Pipeline helpers that resolve a provider; only the
// door (door.go) may reference them, so no door can dispatch around enterDoor.
var doorDispatchFns = map[string]bool{"capability": true, "embedCapability": true, "countCapability": true, "streamCapability": true}

// doorHelperFns are door.go's own dispatch helpers and the stream runner. Only
// the functions in doorHelperCallers may reference them.
var doorHelperFns = map[string]bool{"doorChat": true, "doorEmbed": true, "doorCount": true, "doorStream": true, "withFailover": true, "runStream": true}

// doorHelperCallers are the six doors plus the two stream internals. Every one
// but runStream (it is handed a doorPass by executeStream) must itself call
// enterDoor, and only door.go may name the doorPass type.
var doorHelperCallers = map[string]bool{
	"Executor.Execute": true, "Executor.executeStream": true, "Executor.runStream": true,
	"Executor.Chat": true, "Executor.Embed": true, "Executor.Count": true, "Executor.Stream": true,
}

// callNames lists the selector names called inside body.
func callNames(body ast.Node) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				names[sel.Sel.Name] = true
			}
		}
		return true
	})
	return names
}

// helperRefs lists the door helpers referenced anywhere in n, as a call, a
// method value or a method expression alike.
func helperRefs(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(x ast.Node) bool {
		if sel, ok := x.(*ast.SelectorExpr); ok && doorHelperFns[sel.Sel.Name] {
			out = append(out, sel.Sel.Name)
		}
		return true
	})
	return out
}

// doorViolations lists the places in file (repo-relative path rel) that
// bypass the chokepoint: any reference to a Pipeline dispatch helper or to the
// Resolver field outside door.go and pipeline.go; a door helper referenced
// outside one of the doors (including at package level); a door that
// dispatches without calling enterDoor; the doorPass type named outside
// door.go (except runStream's parameter); or ExecuteStream called from outside
// the package (callers use ExecuteStreamJob).
func doorViolations(rel string, file *ast.File) []string {
	slash := filepath.ToSlash(rel)
	inDoor := slash == "internal/conductor/door.go"
	inConductor := strings.HasPrefix(slash, "internal/conductor/")
	// runStream's signature is the one place outside door.go that names doorPass.
	var allowedPass [2]token.Pos
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && callgraphFuncName(fd) == "Executor.runStream" {
			allowedPass = [2]token.Pos{fd.Type.Pos(), fd.Type.End()}
		}
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			switch {
			case !inDoor && inConductor && doorDispatchFns[x.Sel.Name]:
				out = append(out, slash+": references "+x.Sel.Name+" outside door.go")
			case !inDoor && inConductor && slash != "internal/conductor/pipeline.go" && x.Sel.Name == "Resolver":
				out = append(out, slash+": reads the Resolver outside door.go and pipeline.go")
			case !inConductor && x.Sel.Name == "ExecuteStream":
				out = append(out, slash+": calls ExecuteStream directly (use ExecuteStreamJob)")
			}
		case *ast.Ident:
			if x.Name == "doorPass" && !inDoor && inConductor && (x.Pos() < allowedPass[0] || x.Pos() >= allowedPass[1]) {
				out = append(out, slash+": names doorPass outside door.go")
			}
		}
		return true
	})
	if inDoor || !inConductor {
		return out
	}
	return append(out, doorFuncViolations(slash, file)...)
}

// doorFuncViolations checks every declaration of a conductor file outside
// door.go: a door helper may be referenced only inside one of the doors, and
// every door but runStream must call enterDoor.
func doorFuncViolations(slash string, file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			for _, h := range helperRefs(decl) {
				out = append(out, slash+": references "+h+" at package level")
			}
			continue
		}
		if fd.Body == nil {
			continue
		}
		name, calls := callgraphFuncName(fd), callNames(fd.Body)
		for _, helper := range helperRefs(fd.Body) {
			switch {
			case !doorHelperCallers[name]:
				out = append(out, slash+": "+name+" references "+helper+" but is not a door")
			case name != "Executor.runStream" && !calls["enterDoor"]:
				out = append(out, slash+": "+name+" references "+helper+" without calling enterDoor")
			}
		}
	}
	return out
}

// doorTreeViolations scans every non-test .go file of the module.
func doorTreeViolations(t *testing.T) []string {
	t.Helper()
	root := seamModuleRoot(t)
	fset := token.NewFileSet()
	var bad []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if seamSkipsDir(d.Name()) || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		bad = append(bad, doorViolations(rel, file)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking module tree: %v", walkErr)
	}
	return bad
}

// TestDoorIsOnlyDispatchPath proves the chokepoint cannot be walked around:
// the real tree is clean and each bypass shape, seeded as a fixture, is red.
func TestDoorIsOnlyDispatchPath(t *testing.T) {
	if bad := doorTreeViolations(t); len(bad) > 0 {
		t.Fatalf("calls bypassing the door:\n  %s", strings.Join(bad, "\n  "))
	}
	for _, s := range doorBypassSeeds {
		file, err := parser.ParseFile(token.NewFileSet(), s.rel, s.src, 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", s.name, err)
		}
		if got := doorViolations(s.rel, file); len(got) != 1 {
			t.Errorf("seed %q: violations = %v, want exactly one", s.name, got)
		}
	}
	ok, _ := parser.ParseFile(token.NewFileSet(), "door.go", doorPre+"func (e *Executor) d() { e.pipeline.capability() }", 0)
	if got := doorViolations("internal/conductor/door.go", ok); len(got) != 0 {
		t.Errorf("door.go itself was flagged: %v", got)
	}
}

const doorPre = "package conductor\n"

// doorBypassSeeds are bypass shapes, each of which must yield one violation.
var doorBypassSeeds = []struct{ name, rel, src string }{
	{"leaf door dispatches directly", "internal/conductor/chat.go",
		doorPre + "func (e *Executor) Chat() { e.pipeline.capability()(nil, nil, nil) }"},
	{"a second Resolve", "internal/conductor/stream.go",
		doorPre + "func (e *Executor) x() { e.pipeline.cfg.Resolver.Resolve(nil, nil) }"},
	{"a door helper from a new door", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { e.doorChat(nil, sel, req) }"},
	{"a door helper as a method value", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { f := e.doorChat; f(nil, sel, req) }"},
	{"a door helper as a method expression", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { f := (*Executor).doorChat; f(e, nil, sel, req) }"},
	{"a door helper in a package-level func literal", "internal/conductor/chat2.go",
		doorPre + "var bypass = func(e *Executor) { e.doorChat(nil, sel, req) }"},
	{"a method value of a dispatch helper", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { f := e.pipeline.capability; f() }"},
	{"an aliased Resolver", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { r := e.pipeline.cfg.Resolver; r.Resolve(nil, nil) }"},
	{"a forged doorPass", "internal/conductor/chat.go",
		doorPre + "func (e *Executor) Chat() { e.enterDoor(nil, nil); e.runStream(nil, req, id, doorPass{}, ch) }"},
	{"a zero doorPass variable", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { var p doorPass; _ = p }"},
	{"a new(doorPass)", "internal/conductor/chat2.go",
		doorPre + "func (e *Executor) Chat2() { _ = new(doorPass) }"},
	{"a door that skips enterDoor", "internal/conductor/chat.go",
		doorPre + "func (e *Executor) Chat() { e.doorChat(nil, sel, req) }"},
	{"ExecuteStream from cmd", "cmd/cascade/run.go",
		"package main\nfunc run() { exec.ExecuteStream(nil, nil) }"},
}
