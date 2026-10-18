package conductor_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// localityRow is one host with the BaseURL that carries it to the router and
// the endpoint host that carries it to the topology ("-" = no endpoint
// spelling exists for that URL).
type localityRow struct {
	name    string
	baseURL string
	epHost  string
	local   bool
}

func localityTable() []localityRow {
	return append(unixLocalityRows(), networkLocalityRows()...)
}

// unixLocalityRows are the unix-socket rows of localityTable.
func unixLocalityRows() []localityRow {
	return []localityRow{
		{"unix socket url form", "unix:///run/x.sock", "unix:///run/x.sock", true},
		{"unix socket prefix form", "unix:/run/x.sock", "unix:/run/x.sock", true},
		{"unix relative path", "unix:x.sock", "unix:x.sock", false},
		{"unix host and port", "unix:evil.com:443", "unix:evil.com:443", false},
		{"unix authority form", "unix://evil.com/x", "unix://evil.com/x", false},
		{"unix loopback authority", "unix://localhost/x.sock", "unix://localhost/x.sock", false},
		{"unix userinfo", "unix://@/run/x.sock", "unix://@/run/x.sock", false},
		{"unix blank", "unix: ", "unix: ", false},
		{"unix nul", "unix:\x00", "unix:\x00", false},
		{"unix empty path", "unix:", "unix:", false},
		{"unix url empty path", "unix://", "unix://", false},
		{"unix socket with space", "unix:/run/a b.sock", "unix:/run/a b.sock", false},
		{"unix url socket with space", "unix:///run/a b.sock", "unix:///run/a b.sock", false},
		{"unix root only", "unix:/", "unix:/", false},
		{"unix root after clean", "unix:///.", "unix:///.", false},
		{"unix doubled slash", "unix:////run/x.sock", "unix:////run/x.sock", false},
		{"unix dot-dot segment", "unix:/run/../x.sock", "unix:/run/../x.sock", false},
		{"unix encoded space", "unix:/run/a%20b.sock", "unix:/run/a%20b.sock", false},
		{"unix encoded nul", "unix:/%00", "unix:/%00", false},
		{"unix fragment", "unix:/run/x.sock#f", "unix:/run/x.sock#f", false},
		{"unix url root only", "unix:///", "unix:///", false},
		{"uppercase unix scheme", "UNIX:///run/x.sock", "UNIX:///run/x.sock", false},
		{"unix encoded vertical tab", "unix:/run/a%0bb.sock", "unix:/run/a%0bb.sock", false},
		{"unix encoded no-break space", "unix:/run/a%C2%A0b.sock", "unix:/run/a%C2%A0b.sock", false},
	}
}

// networkLocalityRows are the network-host and endpoint-only rows of
// localityTable.
func networkLocalityRows() []localityRow {
	return []localityRow{
		{"loopback v4", "http://127.0.0.1:8080", "127.0.0.1", true},
		{"loopback range", "http://127.1.2.3", "127.1.2.3", true},
		{"loopback v6", "http://[::1]:1234", "::1", true},
		{"loopback name", "http://localhost:1234", "localhost", true},
		{"padded port", "http://127.0.0.1:00080", "-", false},
		{"port out of range", "http://127.0.0.1:65536", "-", false},
		{"no scheme", "//127.0.0.1", "-", false},
		{"leading zero octet", "http://127.0.0.01", "127.0.0.01", false},
		{"padded octets", "http://127.000.000.001", "127.000.000.001", false},
		{"octal-looking octet", "http://127.0.0.010", "127.0.0.010", false},
		{"short dotted form", "http://127.1", "127.1", false},
		{"five octets", "http://127.0.0.1.5", "127.0.0.1.5", false},
		{"octet over 255", "http://127.0.0.256", "127.0.0.256", false},
		{"loopback zero host", "http://127.0.0.0", "127.0.0.0", true},
		{"uppercase localhost", "http://LOCALHOST:1234", "LOCALHOST", false},
		{"unbracketed v6 loopback", "http://::1", "-", false},
		{"bracketed endpoint host", "-", "[::1]", false},
		{"endpoint host with a fragment", "-", "127.0.0.1#x", false},
		{"endpoint host with userinfo", "-", "127.0.0.1@evil.com", false},
		{"endpoint host with a path", "-", "localhost/x", false},
		{"masquerading domain", "http://127.evil.com", "127.evil.com", false},
		{"masquerading subdomain", "http://127.0.0.1.evil.com", "127.0.0.1.evil.com", false},
		{"private v4", "http://10.0.0.5", "10.0.0.5", false},
		{"lan v4", "http://192.168.1.2", "192.168.1.2", false},
		{"invalid tld", "https://host.invalid", "host.invalid", false},
		{"empty", "", "", false},
	}
}

// TestHostIsLoopbackSharedPredicate: the router's lane classification and
// topology.EndpointIsLocal agree on every row (both are topology.URLIsLocal
// underneath), and exactly one non-test function under internal/ spells the
// 127.0.0.0/8 literal rule and exactly one the unix-socket rule.
func TestHostIsLoopbackSharedPredicate(t *testing.T) {
	for _, r := range localityTable() {
		t.Run(r.name, func(t *testing.T) {
			if r.baseURL != "-" {
				routerLocal := conductor.ClassifyLane(provider.ProviderInfo{Name: "p", BaseURL: r.baseURL}) == conductor.LaneControllerLocal
				urlLocal := topology.URLIsLocal(r.baseURL)
				if routerLocal != r.local || urlLocal != r.local {
					t.Errorf("router local=%v, URLIsLocal=%v for %q, want %v", routerLocal, urlLocal, r.baseURL, r.local)
				}
			}
			if r.epHost != "-" {
				topoLocal := topology.EndpointIsLocal(topology.RuntimeProfile{Endpoint: topology.Endpoint{Host: r.epHost}})
				if topoLocal != r.local {
					t.Errorf("EndpointIsLocal(%q) = %v, want %v", r.epHost, topoLocal, r.local)
				}
			}
		})
	}
	// Control: the matcher catches each shape in a synthetic second
	// predicate and passes a function that holds none of them.
	src := "package x\nfunc a(h string) bool { return strings.Split(h, \".\")[0] == \"127\" }\n" +
		"func b(h string) bool { return strings.HasPrefix(h, \"127.0.\") }\n" +
		"func c(h string) bool { return strings.HasPrefix(h, \"unix://\") }\n" +
		"func d(h string) bool { return h == \"128\" || strings.HasPrefix(h, \"http:\") }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, decl := range f.Decls {
		if got := spellsLocalityRule(decl.(*ast.FuncDecl)); got != (i < 3) {
			t.Errorf("synthetic func %d: spellsLocalityRule = %v, want %v", i, got, i < 3)
		}
	}
	want := []string{"isLoopbackIPv4Literal", "isUnixSocketURL"}
	if got := localityRuleSpellers(t); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("functions under internal/ spelling a loopback or unix-socket rule = %v, want exactly %v", got, want)
	}
}

// localityRuleSpellers returns, sorted, the names of non-test functions
// under internal/ that compare against the string literal "127", pass a
// literal starting "127." to a *Prefix helper, or pass a literal starting
// "unix:" to a *Prefix helper: the shapes of a second locality predicate.
func localityRuleSpellers(t *testing.T) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && spellsLocalityRule(fn) {
				names = append(names, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	sort.Strings(names)
	return names
}

// spellsLocalityRule reports whether fn holds one of localityRuleSpellers'
// shapes.
func spellsLocalityRule(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			found = found || stringLit(x.X) == "127" || stringLit(x.Y) == "127"
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(sel.Sel.Name, "Prefix") {
				return true
			}
			for _, a := range x.Args {
				s := stringLit(a)
				found = found || strings.HasPrefix(s, "127.") || strings.HasPrefix(s, "unix:")
			}
		}
		return true
	})
	return found
}

// stringLit returns the unquoted value of a string literal, or "".
func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}
