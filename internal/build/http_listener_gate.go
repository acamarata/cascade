package build

// Purpose: UnlistedHTTPListeners keeps every listener, serve call and HTTP
//   server or mux value at a known composition point, so a new SSE or RPC
//   surface cannot be served on a mux that skips the daemon's request guard.
// Inputs: a module root. Outputs: sorted "file:line: what" findings, or an
//   error when a build configuration fails to load, parse or type-check, or
//   when no configuration type-checks a file that imports a listener package.
// Constraints: go/types resolution, never string patterns: an alias, a
//   method value or an embedded *http.Server resolve to the same object, and
//   a server or mux value is reported wherever it flows (variable, field,
//   call result, pointer or container element), so serving one through an
//   interface still meets a reported value. Non-test code only;
//   providers/agents/** belongs to ScanAgentDriverNetSurface. Each GOOS loads
//   in full; each extra tag loads the packages holding a file naming it.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// httpListenerAllowed is the gate table: an exact slash path, or a trailing
// "/**" for a directory tree, with its reason.
var httpListenerAllowed = []struct{ Pattern, Reason string }{
	{"internal/daemon/daemon.go", "NewRPCServer: the daemon mux, every route through rpc.Handler"},
	{"internal/daemon/lifecycle_unix.go", "RunOptions.Server: the one server daemon.Run serves"},
	{"internal/daemon/lifecycle_unix_serve.go", "the daemon accept loop serving that server on its socket"},
	{"internal/daemon/lifecycle_unix_handoff.go", "the upgrade handoff passes that socket's listener on"},
	{"internal/runtime/socket_unix.go", "ListenOwnerSocket: the owner-only unix socket bind"},
	{"internal/runtime/socket_lock_unix.go", "that bind's listener, rebuilt from the locked socket fd"},
	{"internal/runtime/socket_windows.go", "ListenOwnerSocket: the same bind's Windows half"},
	{"cmd/cascade/daemon_unix.go", "composeDaemon hands buildRPCServer's server to daemon.Run"},
	{"cmd/cascade/daemon_unix_run.go", "buildRPCServer returns daemon.NewRPCServer's server"},
	{"cmd/cascade/mcp.go", "mcp serve --socket: rpc.NewHandler with rpc.ConnContext"},
	{"cmd/cascade/node_serve.go", "node serve: nodes.ServeRegistry handler with ConnContext"},
	{"internal/providers/agents/egressproxy/**", "per-spawn loopback egress proxy"},
	{"internal/secrets/oauth_transport.go", "one-shot OAuth loopback callback"},
	{"plugins/github/transport.go", "one-shot OAuth loopback callback"},
	{"internal/nodes/tunnel.go", "controller tunnel over SSH"},
	{"internal/nodes/reconnect.go", "controller tunnel re-listen after reconnect"},
}

// httpListenerFuncs names every function or method the gate reports, in
// types.Func.FullName form.
var httpListenerFuncs = httpListenerSet(`net.Listen net.ListenTCP net.ListenUnix net.ListenPacket
	net.ListenUDP net.ListenUnixgram net.ListenIP net.ListenMulticastUDP (*net.ListenConfig).Listen
	(*net.ListenConfig).ListenPacket net.FileListener net.FileConn net.FilePacketConn syscall.Listen
	golang.org/x/sys/unix.Listen crypto/tls.Listen crypto/tls.NewListener net/http.ListenAndServe
	net/http.ListenAndServeTLS net/http.Serve net/http.ServeTLS net/http.NewServeMux net/http/fcgi.Serve
	(*net/http.Server).Serve (*net/http.Server).ServeTLS (*net/http.Server).ListenAndServe
	(*net/http.Server).ListenAndServeTLS (*golang.org/x/net/http2.Server).ServeConn
	net/http/httptest.NewServer net/http/httptest.NewTLSServer net/http/httptest.NewUnstartedServer
	(*golang.org/x/crypto/ssh.Client).Listen (*golang.org/x/crypto/ssh.Client).ListenTCP
	(*golang.org/x/crypto/ssh.Client).ListenUnix net/http.Handle net/http.HandleFunc`)

// httpListenerValueTypes are reported wherever a value flows (so is a wrapped
// listener); httpListenerPkgs make a file one the gate must type-check.
var (
	httpListenerValueTypes = httpListenerSet("net/http.Server net/http.ServeMux net.Listener")
	httpListenerPkgs       = httpListenerSet(`net net/http net/http/fcgi net/http/httptest crypto/tls syscall
		golang.org/x/sys/unix golang.org/x/net/http2 golang.org/x/crypto/ssh`)
)

// httpListenerSet builds a set from whitespace-separated names.
func httpListenerSet(names string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Fields(names) {
		out[name] = true
	}
	return out
}

var (
	httpListenerGOOS = []string{"darwin", "linux", "windows"}
	httpListenerTags = []string{"integration", "pam", "devkeys", "postgres"}
	httpListenerDirs = []string{"cmd", "internal", "pkg", "plugins", "providers"}
)

// UnlistedHTTPListeners reports every listener, serve call, mux or server
// value in non-test code under root outside httpListenerAllowed.
func UnlistedHTTPListeners(root string) ([]string, error) {
	var patterns []string
	for _, dir := range httpListenerDirs {
		if fi, err := os.Stat(filepath.Join(root, dir)); err == nil && fi.IsDir() {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	tagDirs, imports, err := httpListenerWalk(root, patterns)
	if err != nil || len(patterns) == 0 {
		return nil, fmt.Errorf("http listener gate: nothing to scan under %s (%v)", root, err)
	}
	found, checked, scanned := map[string]bool{}, map[string]bool{}, 0
	for _, goos := range httpListenerGOOS {
		for _, tag := range append([]string{""}, httpListenerTags...) {
			dirs := patterns
			if tag != "" {
				dirs = tagDirs[tag]
			}
			if len(dirs) == 0 {
				continue
			}
			n, err := httpListenerLoad(root, goos, tag, dirs, found, checked)
			if err != nil {
				return nil, err
			}
			scanned += n
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("http listener gate: no package loaded under %s", root)
	}
	// A listener-importing file no configuration type-checked (cgo, another
	// GOARCH, a tag conjunction, an unknown tag) is one the gate cannot vouch for.
	for _, rel := range slices.Sorted(maps.Keys(imports)) {
		if !checked[rel] && !strings.HasPrefix(rel, "providers/agents/") {
			return nil, fmt.Errorf("http listener gate: %s imports %s and no build configuration type-checks it", rel, imports[rel])
		}
	}
	return slices.Sorted(maps.Keys(found)), nil
}

// httpListenerWalk parses the imports and build lines of every non-test .go
// file under patterns. It maps each tag in httpListenerTags to the "./<dir>"
// package directories holding a file whose //go:build line names it, and
// each file importing a listener package to that package.
func httpListenerWalk(root string, patterns []string) (map[string][]string, map[string]string, error) {
	seen, imports := map[[2]string]bool{}, map[string]string{}
	for _, pattern := range patterns {
		start := filepath.Join(root, strings.TrimSuffix(pattern, "/..."))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if err == nil && p != start && egressSkippedDir(d.Name()) {
					return filepath.SkipDir
				}
				return err
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			return httpListenerNoteFile(root, p, seen, imports)
		})
		if err != nil {
			return nil, nil, err
		}
	}
	out := map[string][]string{}
	for _, k := range slices.SortedFunc(maps.Keys(seen), func(a, b [2]string) int { return strings.Compare(a[1], b[1]) }) {
		out[k[0]] = append(out[k[0]], k[1])
	}
	return out, imports, nil
}

// httpListenerNoteFile records one file's gate tags and listener import.
func httpListenerNoteFile(root, file string, seen map[[2]string]bool, imports map[string]string) error {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); httpListenerPkgs[p] && imports[rel] == "" {
			imports[rel] = p
		}
	}
	for _, group := range f.Comments {
		for _, c := range group.List {
			body, ok := strings.CutPrefix(c.Text, "//go:build ")
			for _, word := range strings.FieldsFunc(body, func(r rune) bool { return strings.ContainsRune(" \t()!&|", r) }) {
				if ok && slices.Contains(httpListenerTags, word) {
					seen[[2]string{word, "./" + path.Dir(rel)}] = true
				}
			}
		}
	}
	return nil
}

// httpListenerLoad type-checks one GOOS and tag, records reported uses and
// checked files, and counts scanned packages; any load or type error fails.
func httpListenerLoad(root, goos, tag string, patterns []string, found, checked map[string]bool) (int, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir: root,
		Env: append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0"),
	}
	if tag != "" {
		cfg.BuildFlags = []string{"-tags=" + tag}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return 0, fmt.Errorf("http listener gate: load %s/%s: %w", goos, tag, err)
	}
	scanned := 0
	for _, pkg := range pkgs {
		if httpListenerExcluded(pkg) {
			continue
		}
		if len(pkg.Errors) != 0 {
			return 0, fmt.Errorf("http listener gate: %s/%s %s: %v", goos, tag, pkg.PkgPath, pkg.Errors[0])
		}
		if len(pkg.Syntax) == 0 || len(pkg.Syntax) != len(pkg.CompiledGoFiles) {
			return 0, fmt.Errorf("http listener gate: %s/%s %s: incomplete syntax", goos, tag, pkg.PkgPath)
		}
		for _, file := range pkg.Syntax {
			rel, err := filepath.Rel(root, pkg.Fset.Position(file.Pos()).Filename)
			if err != nil {
				return 0, err
			}
			rel = filepath.ToSlash(rel)
			checked[rel] = true
			if strings.HasPrefix(rel, "providers/agents/") || httpListenerIsAllowed(rel) {
				continue
			}
			httpListenerFile(pkg, file, rel, found)
		}
		scanned++
	}
	return scanned, nil
}

// httpListenerExcluded reports a package with no non-test Go file in this
// configuration (nothing compiles, nothing to scan); no other error is excused.
func httpListenerExcluded(pkg *packages.Package) bool {
	return len(pkg.GoFiles) == 0 && (len(pkg.Errors) == 0 ||
		len(pkg.Errors) == 1 && strings.Contains(pkg.Errors[0].Msg, "build constraints exclude all Go files"))
}

// httpListenerIsAllowed matches rel against the gate table.
func httpListenerIsAllowed(rel string) bool {
	for _, row := range httpListenerAllowed {
		dir, tree := strings.CutSuffix(row.Pattern, "/**")
		if (tree && strings.HasPrefix(rel, dir+"/")) || (!tree && rel == row.Pattern) {
			return true
		}
	}
	return false
}

// httpListenerFile records every resolved use in one file: a listed
// function or method, and any defined name or value expression whose type
// holds a server or mux.
func httpListenerFile(pkg *packages.Package, file *ast.File, rel string, found map[string]bool) {
	report := func(n ast.Node, what string) {
		found[fmt.Sprintf("%s:%d: %s", rel, pkg.Fset.Position(n.Pos()).Line, what)] = true
	}
	info := pkg.TypesInfo
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if fn, ok := info.Uses[id].(*types.Func); ok && httpListenerFuncs[fn.FullName()] {
				report(id, fn.FullName())
			}
			if v, ok := info.Defs[id].(*types.Var); ok && httpListenerValueTypes[httpListenerTypeName(v.Type())] {
				report(id, "value of "+httpListenerTypeName(v.Type()))
			}
		}
		if e, ok := n.(ast.Expr); ok {
			if tv, ok := info.Types[e]; ok && tv.IsValue() && httpListenerValueTypes[httpListenerTypeName(tv.Type)] {
				report(e, "value of "+httpListenerTypeName(tv.Type))
			}
		}
		return true
	})
}

// httpListenerTypeName renders the named type a value of t holds, through
// pointers and slice, array, map and channel elements, as "<pkg>.<Name>",
// or "" for anything else.
func httpListenerTypeName(t types.Type) string {
	for {
		switch u := types.Unalias(t).(type) {
		case interface{ Elem() types.Type }: // pointer, slice, array, map value, channel
			t = u.Elem()
		case *types.Named:
			if u.Obj().Pkg() == nil {
				return ""
			}
			return u.Obj().Pkg().Path() + "." + u.Obj().Name()
		default:
			return ""
		}
	}
}
