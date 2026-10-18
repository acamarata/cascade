package topology

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestEndpointIsLocal(t *testing.T) {
	local := []string{"127.0.0.1", "127.1.2.3", "::1", "localhost", "unix:/run/x.sock", "unix:///run/x.sock", "127.0.0.0"}
	remote := []string{"unix:x.sock", "unix:evil.com:443", "unix://evil.com/x", "unix: ", "unix:\x00", "unix:/", "127.0.0.01", "127.000.000.001", "127.0.0.010", "127.1", "127.evil.com", "10.0.0.5", "192.168.1.2", "host.invalid", "", "unix:", "LOCALHOST", "127.0.0", "127.0.0.1.5", "127.0.0.256", "[::1]",
		"127.0.0.1#x", "localhost/x", "127.0.0.1@evil.com", "127.0.0.1?x", "unix:////run/x.sock", "unix:/run/a%20b.sock"}
	for _, persistent := range []bool{true, false} {
		for _, h := range local {
			p := RuntimeProfile{ID: "p", Runtime: RuntimeAPI, Persistent: persistent, Endpoint: Endpoint{Host: h}}
			if !EndpointIsLocal(p) {
				t.Errorf("persistent=%v host %q must be local", persistent, h)
			}
		}
		for _, h := range remote {
			p := RuntimeProfile{ID: "p", Runtime: RuntimeOllama, Persistent: persistent, Endpoint: Endpoint{Host: h, Port: 80}}
			if EndpointIsLocal(p) {
				t.Errorf("persistent=%v host %q must be remote", persistent, h)
			}
		}
	}
}

// TestURLIsLocal pins the router-facing spellings: canonical host:port
// forms only, the same as Go's HTTP proxy selection treats as loopback.
func TestURLIsLocal(t *testing.T) {
	local := []string{"http://127.0.0.1", "http://127.0.0.1:8080", "https://localhost:1", "http://[::1]", "http://[::1]:11434",
		"unix:///run/x.sock", "unix:/run/x.sock", "ftp://127.255.255.255", "http://127.0.0.1:0", "http://127.0.0.1:65535"}
	remote := []string{"", "://x", "https://", "http://127.0.0.1:", "http://localhost:", "http://[::1]:", "http://[::1", "http://[::1]x",
		"http://::1", "http://::1:8080", "http://127.0.0.1:80a", "http://LOCALHOST", "http://Localhost:1", "http://127.000.000.001",
		"http://127.0.0.1@evil.com", "http://evil.com@127.0.0.1x", "http://126.255.255.255", "http://128.0.0.1", "http://[0:0:0:0:0:0:0:1]",
		"unix://evil.com/x", "unix://localhost/x", "unix://@/x", "unix://user@/x", "unix://", "unix:///", "unix:", "unix:x", "UNIX:///x",
		"unix:///a b", "unix:///a\tb", "unix:////run/x.sock", "unix:/run/../x.sock", "unix:///.", "unix:////", "unix:///..",
		"unix:////..", "unix:/run/x/", "unix:/%00", "unix:/run/a%20b.sock", "unix:/%2e%2e/", "unix:/#x", "unix:/?x", "unix:/run/x?",
		"unix://?q", "unix://#f", "unix:/run/a%0bb.sock", "unix:/run/a%0cb.sock", "unix:/run/a%C2%A0b.sock", "unix:/run/a%7fb.sock", "http://127.0.0.1:00080", "http://127.0.0.1:65536", "http://127.0.0.1:123456", "//127.0.0.1", "127.0.0.1"}
	for _, u := range local {
		if !URLIsLocal(u) {
			t.Errorf("URLIsLocal(%q) = false, want true", u)
		}
	}
	for _, u := range remote {
		if URLIsLocal(u) {
			t.Errorf("URLIsLocal(%q) = true, want false", u)
		}
	}
}

// TestCanonicalHostRefusesUnbracketedIPv6: net/url rejects "http://::1:8080"
// today, so URLIsLocal never reaches this rule through a parse; the rule
// still holds for a net/url that accepts the spelling. Every unbracketed
// host holding ':' is refused outright, not just the one spelling "::1".
func TestCanonicalHostRefusesUnbracketedIPv6(t *testing.T) {
	for _, hp := range []string{"::1", "::1:", "::1:8080", "::", "fe80::1:80"} {
		if h, ok := canonicalHost(hp); ok {
			t.Errorf("canonicalHost(%q) = %q, true; want refused", hp, h)
		}
	}
	if h, ok := canonicalHost("[::1]:8080"); !ok || h != "::1" {
		t.Errorf("canonicalHost([::1]:8080) = %q, %v; want ::1, true", h, ok)
	}
}

// parseTopologyDir parses every non-test Go file of this package directory.
func parseTopologyDir(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		files = append(files, f)
	}
	return files
}

// localityViolations walks root and every same-package function or method
// it calls (transitively), and reports each read of a runtime kind, a
// profile id or the Persistent flag, plus the names of the functions it
// inspected. Callees outside the package are not inspected.
func localityViolations(files []*ast.File, root string) (violations, visited []string) {
	decls := map[string][]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				decls[fn.Name.Name] = append(decls[fn.Name.Name], fn)
			}
		}
	}
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, fn := range decls[name] {
			visited = append(visited, name)
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.Ident: // a selector's field name is an Ident too
					if forbiddenLocalityName(x.Name) {
						violations = append(violations, name+" uses "+x.Name)
					}
				case *ast.CallExpr:
					queue = append(queue, calleeName(x))
				}
				return true
			})
		}
	}
	return violations, visited
}

// calleeName returns the called function or method name of c ("" if none).
func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// forbiddenLocalityName: the runtime kind, the profile id and the persistence
// flag must not feed the locality decision.
func forbiddenLocalityName(n string) bool {
	return n == "ID" || n == "Persistent" || strings.HasPrefix(n, "Runtime")
}

// TestLocalityNeverKeysOnRuntimeName: the decision reads the endpoint only,
// in EndpointIsLocal and in every same-package function it reaches.
func TestLocalityNeverKeysOnRuntimeName(t *testing.T) {
	violations, visited := localityViolations(parseTopologyDir(t), "EndpointIsLocal")
	if len(visited) == 0 || visited[0] != "EndpointIsLocal" {
		t.Fatalf("inspected %v, want EndpointIsLocal first", visited)
	}
	for _, v := range violations {
		t.Error(v)
	}
	// Negative control: a helper in another file that reads Persistent or
	// the runtime kind must be caught through the call graph.
	src := "package topology\nfunc EndpointIsLocal(p RuntimeProfile) bool { return helper(p) }\n" +
		"func helper(p RuntimeProfile) bool { return p.Persistent || p.Runtime == RuntimeOllama }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := localityViolations([]*ast.File{f}, "EndpointIsLocal")
	if len(got) != 3 {
		t.Errorf("synthetic helper violations = %v, want 3 (Persistent, Runtime, RuntimeOllama)", got)
	}
	lan := RuntimeProfile{Runtime: RuntimeOllama, Endpoint: Endpoint{Host: "10.0.0.5", Port: 11434}}
	api := RuntimeProfile{Runtime: RuntimeAPI, Endpoint: Endpoint{Host: "127.0.0.1", Port: 8080}}
	cli := RuntimeProfile{Runtime: RuntimeClaudeCLI}
	if EndpointIsLocal(lan) || !EndpointIsLocal(api) || EndpointIsLocal(cli) {
		t.Errorf("lan ollama=%v (want false), loopback api=%v (want true), spawned cli=%v (want false)",
			EndpointIsLocal(lan), EndpointIsLocal(api), EndpointIsLocal(cli))
	}
}
