package learn

// Purpose: shared helpers for the learned-config tests (home isolation with
//   its guard, error identity + message assertion) and the closed-vocabulary
//   and constructor tests of config.go.
// SPORT: learn/config_test (P1-LRN-01).

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// operatorHome is the HOME the test process started with; no test may run
// with it.
var operatorHome = os.Getenv("HOME")

// homeVars are the variables a learned-config test points at a fresh dir.
var homeVars = []string{"HOME", "USERPROFILE", "CASCADE_HOME"}

// isolateHome points HOME, USERPROFILE and CASCADE_HOME at a fresh
// t.TempDir() and proves it with requireIsolatedHome.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range homeVars {
		t.Setenv(k, dir)
	}
	requireIsolatedHome(t, dir)
	return dir
}

// requireIsolatedHome refuses a test whose home variables are not exactly
// dir, not under the temp root, or equal to the operator's HOME.
func requireIsolatedHome(t *testing.T, dir string) {
	t.Helper()
	for _, k := range homeVars {
		v := os.Getenv(k)
		rel, err := filepath.Rel(os.TempDir(), v)
		if v != dir || err != nil || strings.HasPrefix(rel, "..") || (operatorHome != "" && v == operatorHome) {
			t.Fatalf("home guard: %s is not an isolated temp dir", k)
		}
	}
}

// wantErr asserts err is a cascade *Error of kind whose message is exactly
// msg (identity + message, never errors.Is alone).
func wantErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want a cascade %v error", err, kind)
	}
	if ce.Kind != kind || ce.Msg != msg {
		t.Fatalf("error = (%v, %q), want (%v, %q)", ce.Kind, ce.Msg, kind, msg)
	}
}

func TestConfigTierParseFailsClosed(t *testing.T) {
	isolateHome(t)
	for _, s := range []string{"safe", "behavioral", "security"} {
		got, err := ParseConfigTier(s)
		if err != nil || string(got) != s {
			t.Fatalf("ParseConfigTier(%q) = (%q, %v)", s, got, err)
		}
	}
	for _, s := range []string{"", "SAFE", "other", "Safe", " safe"} {
		got, err := ParseConfigTier(s)
		if got != "" {
			t.Fatalf("ParseConfigTier(%q) returned %q beside its error", s, got)
		}
		wantErr(t, err, cascade.KindInvalidInput, "learn: config tier must be safe, behavioral or security")
	}
	if maxTier("bogus", TierSafe) != TierSecurity || maxTier(TierSafe, TierBehavioral) != TierBehavioral {
		t.Fatal("maxTier must rank an unknown tier as security")
	}
}

func TestDenylistAreaClosedSet(t *testing.T) {
	isolateHome(t)
	want := []DenylistArea{"auth_rules", "secret_access", "destructive_permissions", "trust_roots",
		"mandatory_security_gates", "provider_compliance_flags", "model_authoring_capability", "egress_rules",
		"approval_rules", "minimum_verification", "backup_guarantees", "execution_authority"}
	got := DenylistAreas()
	if !slices.Equal(got, want) {
		t.Fatalf("DenylistAreas() = %v, want %v", got, want)
	}
	got[0] = "tampered"
	if DenylistAreas()[0] != AreaAuthRules {
		t.Fatal("DenylistAreas returned the live table, not a copy")
	}
	for _, a := range want {
		if p, err := ParseDenylistArea(string(a)); err != nil || p != a {
			t.Fatalf("ParseDenylistArea(%q) = (%q, %v)", a, p, err)
		}
	}
	for _, s := range []string{"", "AUTH_RULES", "policy", "network"} {
		_, err := ParseDenylistArea(s)
		wantErr(t, err, cascade.KindInvalidInput, "learn: unknown denylist area")
	}
	assertClosedTables(t)
}

// closedNames are package tables no code may write outside their declaration.
var closedNames = []string{"denylistAreaTable", "denyRuleTable", "defaultRegistry"}

// assertClosedTables parses every non-test file of the package and refuses
// any write to a closed table, and any registry map/slice write outside
// newRegistry. Each closed name must be declared once and read at least
// once, so the scan cannot pass on an empty package.
func assertClosedTables(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	reads := map[string]int{}
	for _, f := range parsePackageFiles(t, fset) {
		for _, d := range f.Decls {
			fd, isFunc := d.(*ast.FuncDecl)
			ast.Inspect(d, func(n ast.Node) bool {
				for _, w := range writtenExprs(n) {
					if name := rootIdent(w); slices.Contains(closedNames, name) {
						t.Errorf("%s: %s is written outside its declaration", fset.Position(w.Pos()), name)
					}
					if sel := registryField(w); sel != "" && (!isFunc || fd.Name.Name != "newRegistry") {
						t.Errorf("%s: registry field %s written outside newRegistry", fset.Position(w.Pos()), sel)
					}
				}
				if id, ok := n.(*ast.Ident); ok && slices.Contains(closedNames, id.Name) {
					reads[id.Name]++
				}
				return true
			})
		}
	}
	for _, name := range closedNames {
		if reads[name] < 2 {
			t.Errorf("closed-table scan saw %s %d times; the scan is not reading the package", name, reads[name])
		}
	}
}

// parsePackageFiles parses every non-test .go file of this package.
func parsePackageFiles(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []*ast.File
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out = append(out, f)
	}
	return out
}

// writtenExprs returns the expressions n writes: assignment and inc/dec
// targets, and the first argument of append.
func writtenExprs(n ast.Node) []ast.Expr {
	switch x := n.(type) {
	case *ast.AssignStmt:
		return x.Lhs
	case *ast.IncDecStmt:
		return []ast.Expr{x.X}
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "append" && len(x.Args) > 0 {
			return x.Args[:1]
		}
	}
	return nil
}

// rootIdent is the base identifier of an index, selector or star chain.
func rootIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// registryField names a write to a registry's targets, byID or aliases.
func registryField(e ast.Expr) string {
	if ix, ok := e.(*ast.IndexExpr); ok {
		e = ix.X
	}
	if sel, ok := e.(*ast.SelectorExpr); ok && slices.Contains([]string{"targets", "byID", "aliases"}, sel.Sel.Name) {
		return sel.Sel.Name
	}
	return ""
}

func TestLearnedConfigConstructorInvariants(t *testing.T) {
	isolateHome(t)
	safe := LearnedConfig{ID: "c1", Target: "ci.local_timeout", Tier: TierSafe, Status: StatusPending,
		Change: Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "120"}}
	sec := LearnedConfig{ID: "c2", Target: "auth.rules", Tier: TierSecurity, Areas: []DenylistArea{AreaAuthRules},
		Status: StatusPending, Change: Change{Op: OpSet, Path: "policy.auth.mode", Value: `"open"`}}
	for _, ok := range []LearnedConfig{safe, sec} {
		if got, err := NewLearnedConfig(ok); err != nil || got.Tier != ok.Tier {
			t.Fatalf("NewLearnedConfig(valid %s) = (%v, %v)", ok.Tier, got.Tier, err)
		}
	}
	const invariant = "learn: tier security must coincide with a denylist area or a loosened bound"
	const mismatch = "learn: stated tier, areas or bound flag disagree with the computed classification"
	cases := []struct {
		name string
		mut  func(c *LearnedConfig)
		base LearnedConfig
		msg  string
	}{
		{"security without area or loosen", func(c *LearnedConfig) { c.Tier = TierSecurity }, safe, invariant},
		{"safe with an area", func(c *LearnedConfig) { c.Areas = []DenylistArea{AreaEgressRules} }, safe, invariant},
		{"safe with loosened bound", func(c *LearnedConfig) { c.LoosensBound = true }, safe, invariant},
		{"behavioral over a denied change", func(c *LearnedConfig) { c.Tier = TierBehavioral }, sec, invariant},
		{"claims an area the change lacks", func(c *LearnedConfig) {
			c.Tier, c.Areas = TierSecurity, []DenylistArea{AreaAuthRules}
		}, safe, mismatch},
		{"claims safe for a loosening value", func(c *LearnedConfig) { c.Change.Value = "900" }, safe, mismatch},
		{"claims the wrong area", func(c *LearnedConfig) { c.Areas = []DenylistArea{AreaEgressRules} }, sec, mismatch},
		{"stores an alias, not the id", func(c *LearnedConfig) { c.Target = "auth_rules" }, sec, mismatch},
	}
	for _, tc := range cases {
		c := tc.base
		c.Areas = slices.Clone(c.Areas)
		tc.mut(&c)
		got, err := NewLearnedConfig(c)
		if got.ID != "" {
			t.Fatalf("%s: a refused config was returned (%+v)", tc.name, got)
		}
		wantErr(t, err, cascade.KindInvalidInput, tc.msg)
	}
	bad := safe
	bad.Tier = ""
	_, err := NewLearnedConfig(bad)
	wantErr(t, err, cascade.KindInvalidInput, "learn: config tier must be safe, behavioral or security")
	bad = safe
	bad.Status = "done"
	_, err = NewLearnedConfig(bad)
	wantErr(t, err, cascade.KindInvalidInput, "learn: status must be pending, applied, rejected or reverted")
}
