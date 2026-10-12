package build

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentDriverNetViolation identifies a forbidden network surface.
type AgentDriverNetViolation struct {
	File string
	Line int
	What string
}

// ScanAgentDriverNetSurface checks non-test files regardless of build tags.
func ScanAgentDriverNetSurface(root string) ([]AgentDriverNetViolation, error) {
	var out []AgentDriverNetViolation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// go build follows an in-module symlinked package dir; refuse, never follow.
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "providers/agents/") || strings.HasPrefix("providers/agents/", rel+"/") {
				out = append(out, AgentDriverNetViolation{File: rel, What: "symlink in the agent driver tree"})
			}
			return nil
		}
		if d.IsDir() {
			if path != root && egressSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "providers/agents/") {
			return nil
		}
		if ext := filepath.Ext(rel); ext == ".s" || ext == ".S" || ext == ".syso" {
			out = append(out, AgentDriverNetViolation{File: rel, Line: 1, What: "forbidden assembly or object file"})
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		violations, err := agentDriverFile(path, rel)
		out = append(out, violations...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// agentDriverFile checks imports and every use of the net package.
func agentDriverFile(path, rel string) ([]AgentDriverNetViolation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []AgentDriverNetViolation
	add := func(n ast.Node, what string) {
		out = append(out, AgentDriverNetViolation{File: rel, Line: fset.Position(n.Pos()).Line, What: what})
	}
	hasNet := false
	for _, imp := range f.Imports {
		name, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		std := name
		if first, _, _ := strings.Cut(name, "/"); !strings.Contains(first, ".") && name != "C" {
			// Std paths are lowercase; a folded spelling resolves on a case-insensitive disk.
			std = strings.ToLower(name)
			if !agentDriverInGoroot(std) {
				// A dotless path outside GOROOT is a foreign module, not std.
				add(imp, "forbidden import "+name)
				continue
			}
			if std != name {
				add(imp, "non-canonical import spelling "+name)
			}
		}
		if agentDriverForbiddenImport(std) {
			add(imp, "forbidden import "+name)
		}
		if agentDriverSkippedImport(name) {
			add(imp, "import of an unscanned directory "+name)
		}
		if std == "net" {
			hasNet = true
			if imp.Name != nil {
				add(imp, "net import must not have an alias")
			}
		}
	}
	if hasNet {
		agentDriverSelectors(f, add)
	}
	return out, nil
}

// agentDriverInGoroot reports whether go/build finds the import path in GOROOT.
func agentDriverInGoroot(path string) bool {
	pkg, err := build.Default.Import(path, "", build.FindOnly)
	return err == nil && pkg.Goroot
}

// agentDriverForbiddenImport bans every package that can open a socket.
// Under net/ only the pure parsers stay legal; "net" itself is checked by
// selector. Prefix families cover subpackages such as net/rpc/jsonrpc. Any
// module outside this one is banned outright, since a third-party package
// can dial on the driver's behalf.
func agentDriverForbiddenImport(name string) bool {
	switch name {
	case "net", "net/url", "net/netip", "net/mail":
		return false
	case "crypto/tls", "syscall", "log/syslog", "C", "unsafe", "plugin":
		return true
	}
	if first, _, _ := strings.Cut(name, "/"); strings.Contains(first, ".") {
		return !strings.HasPrefix(name, "github.com/acamarata/cascade/")
	}
	for _, prefix := range []string{"net/", "golang.org/x/net", "golang.org/x/sys/", "golang.org/x/crypto/ssh"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// agentDriverSkippedImport reports an import into a directory the network
// scanners skip (testdata or a dot directory). Go compiles such a package
// when it is imported by path, so a driver could hide a dial there. The match
// ignores case: on a case-insensitive disk "TestData" resolves to testdata.
func agentDriverSkippedImport(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if egressSkippedDir(strings.ToLower(segment)) {
			return true
		}
	}
	return false
}

// agentDriverSelectors rejects escaped Listen values as well as non-unix calls.
func agentDriverSelectors(f *ast.File, add func(ast.Node, string)) {
	allowed := map[string]bool{"Listener": true, "Conn": true, "Listen": true, "UnixAddr": true, "ErrClosed": true, "OpError": true}
	unixCalls := map[*ast.SelectorExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if ok && lit.Kind == token.STRING {
			value, err := strconv.Unquote(lit.Value)
			unixCalls[sel] = err == nil && value == "unix"
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "net" {
			return true
		}
		if !allowed[sel.Sel.Name] || (sel.Sel.Name == "Listen" && !unixCalls[sel]) {
			add(sel, "forbidden net surface "+sel.Sel.Name)
		}
		return true
	})
}
