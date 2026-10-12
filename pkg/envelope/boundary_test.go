package envelope

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedModuleImports are the only module packages pkg/envelope may use.
var allowedModuleImports = map[string]bool{
	"github.com/acamarata/cascade/pkg/cascade":  true,
	"github.com/acamarata/cascade/pkg/provider": true,
	"github.com/acamarata/cascade/pkg/envelope": true, // the external example package
}

// TestEnvelopeImportBoundary: pkg/envelope (tests included) imports only
// the standard library, pkg/cascade and pkg/provider - never internal/ -
// and the depguard rules keep pkg/ out of internal/ while leaving
// pkg/envelope importable from providers/.
func TestEnvelopeImportBoundary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no package files found (err %v)", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first := strings.SplitN(p, "/", 2)[0]
			if strings.Contains(first, ".") && !allowedModuleImports[p] {
				t.Errorf("%s imports %s: pkg/envelope may import only stdlib, pkg/cascade and pkg/provider", f, p)
			}
			if strings.Contains(p, "/internal") {
				t.Errorf("%s imports internal package %s", f, p)
			}
		}
	}
	cfg, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(cfg)
	for _, want := range []string{"pkg-no-internal:", `"**/pkg/**/*.go"`, `pkg: "github.com/acamarata/cascade/internal"`} {
		if !strings.Contains(text, want) {
			t.Errorf(".golangci.yml lacks %q: the depguard pkg-no-internal rule is gone", want)
		}
	}
	if strings.Contains(text, "cascade/pkg/envelope") {
		t.Error(".golangci.yml names pkg/envelope in a rule: providers/ must stay free to import it")
	}
}
