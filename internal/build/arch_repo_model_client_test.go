// Package build (this file): the one-model-intake gate for internal/repo
// (P1-GEN-02). The inferred-fact extract intake dispatches only through the
// provider.ModelExecutor its caller injects (the daemon's in-process
// conductor door). No shipping file in internal/repo may import an HTTP, RPC
// or daemon client package, construct an http/rpc client, or build a
// provider client, so no second path to a model can appear there.
package build

import (
	"go/ast"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repoForbiddenImports are the packages a second model client would need.
var repoForbiddenImports = map[string]bool{
	"net":             true,
	"net/http":        true,
	"net/rpc":         true,
	"net/rpc/jsonrpc": true,
	"github.com/acamarata/cascade/internal/client":    true,
	"github.com/acamarata/cascade/internal/rpc":       true,
	"github.com/acamarata/cascade/internal/conductor": true,
	"github.com/acamarata/cascade/internal/daemon":    true,
	"github.com/acamarata/cascade/internal/retrieval": true,
}

// repoForbiddenSelectors are constructors of a client on any import name.
var repoForbiddenSelectors = map[string]bool{
	"Client": true, "NewClient": true, "NewRequest": true, "NewRequestWithContext": true,
	"Dial": true, "DialHTTP": true, "DialContext": true, "Post": true, "Get": true,
}

// repoModelClientViolations reports every forbidden import and every client
// construction in the shipping files of internal/repo.
func repoModelClientViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if !underDir(s.rel, "internal/repo") {
			continue
		}
		pkgs := map[string]bool{}
		for _, imp := range s.file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if repoForbiddenImports[p] || strings.HasPrefix(p, "github.com/acamarata/cascade/pkg/provider/") {
				bad = append(bad, s.rel+": imports "+p)
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			pkgs[name] = true
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && pkgs[id.Name] && repoForbiddenSelectors[sel.Sel.Name] {
				bad = append(bad, s.rel+": uses "+id.Name+"."+sel.Sel.Name)
			}
			return true
		})
	}
	sort.Strings(bad)
	return bad
}

func TestArchRepoNoOtherModelClient(t *testing.T) {
	srcs := shippingSources(t, archModuleRoot(t))
	seen := false
	for _, s := range srcs {
		if s.rel == "internal/repo/extract.go" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("internal/repo/extract.go not parsed; the gate would pass vacuously")
	}
	if bad := repoModelClientViolations(srcs); len(bad) > 0 {
		t.Fatalf("internal/repo must reach a model only through the injected executor:\n  %s", strings.Join(bad, "\n  "))
	}
	seeded := seededSources(t, map[string]string{
		"internal/repo/a.go":  "package repo\nimport \"net/http\"\nvar c = &http.Client{}\n",
		"internal/repo/b.go":  "package repo\nimport p \"github.com/acamarata/cascade/pkg/provider\"\nvar c = p.NewClient(nil)\n",
		"internal/repo/c.go":  "package repo\nimport \"github.com/acamarata/cascade/internal/daemon\"\nvar d = daemon.ProcessConductorDoor\n",
		"internal/repo/d.go":  "package repo\nimport \"github.com/acamarata/cascade/pkg/provider/anthropic\"\nvar _ = anthropic.X\n",
		"internal/other/e.go": "package other\nimport \"net/http\"\nvar c = &http.Client{}\n",
	})
	got := repoModelClientViolations(seeded)
	want := []string{
		"internal/repo/a.go: imports net/http",
		"internal/repo/a.go: uses http.Client",
		"internal/repo/b.go: uses p.NewClient",
		"internal/repo/c.go: imports github.com/acamarata/cascade/internal/daemon",
		"internal/repo/d.go: imports github.com/acamarata/cascade/pkg/provider/anthropic",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("seeded violations = %q, want %q", got, want)
	}
}
