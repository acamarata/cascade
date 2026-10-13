package build

// Purpose: the SpawnEnvelope forging gate (P1-AGT-02): Check tests completeness
// only, so this ban is what stops production code building an envelope by hand.
// SPORT: internal.build tests (ADD) - P1-AGT-02.

import (
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// ScanSpawnEnvelopeForging reports, outside allowed files, a SpawnEnvelope
// composite literal (or one of any type shaped like it, a generic included),
// new(SpawnEnvelope), a conversion to it, and a write to or address of one of
// its fields (promoted ones too); and, in every file, an alias of the type.
// Reads stay legal. Decoding (json, reflect) is out of reach of a type check.
func ScanSpawnEnvelopeForging(root string, allowed func(rel string) bool) ([]AgentSpawnViolation, error) {
	hint := func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		return ok && (id.Name == "SpawnEnvelope" || id.Name == "Envelope")
	}
	return agentTypedScan(root, hint, func(pkg *packages.Package, f *ast.File, rel string, report func(ast.Node, string)) {
		info, ok := pkg.TypesInfo, allowed(rel)
		const what = "SpawnEnvelope built or written outside the projection"
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				if x.Assign.IsValid() && isSpawnEnv(info.TypeOf(x.Type)) {
					report(x, "alias of SpawnEnvelope")
				}
			case *ast.CompositeLit:
				if !ok && envShaped(info.TypeOf(x)) {
					report(x, what)
				}
			case *ast.CallExpr:
				if tv := info.Types[x.Fun]; !ok && len(x.Args) == 1 && tv.IsType() && isSpawnEnv(tv.Type) {
					report(x, what)
				} else if id, _ := x.Fun.(*ast.Ident); !ok && id != nil && id.Name == "new" && len(x.Args) == 1 && isSpawnEnv(info.TypeOf(x.Args[0])) {
					report(x, what)
				}
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					if !ok && envField(info, l) {
						report(l, what)
					}
				}
			case *ast.IncDecStmt:
				if !ok && envField(info, x.X) {
					report(x, what)
				}
			case *ast.UnaryExpr:
				if !ok && x.Op == token.AND && envField(info, x.X) {
					report(x, what)
				}
			}
			return true
		})
	})
}

func envForgingAllowed(rel string) bool {
	return path.Dir(rel) == "internal/providers/agents" || strings.HasPrefix(rel, "providers/agents/conformance/")
}

// isSpawnEnv reports whether t is pkg/provider.SpawnEnvelope (or a pointer to it, or an alias).
func isSpawnEnv(t types.Type) bool {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == agentModule+"/pkg/provider" && n.Obj().Name() == "SpawnEnvelope"
}

// envShaped is isSpawnEnv, or any type whose core struct has the envelope's field names.
func envShaped(t types.Type) bool {
	if t == nil {
		return false
	}
	st := coreStruct(t)
	if isSpawnEnv(t) || st == nil {
		return isSpawnEnv(t)
	}
	names := map[string]bool{}
	for i := 0; i < st.NumFields(); i++ {
		names[st.Field(i).Name()] = true
	}
	return names["Digest"] && names["Principal"] && names["Roots"] && names["Egress"] && names["ExpiresAt"]
}

// coreStruct returns t's struct type, looking through a type parameter whose
// constraint is a single ~struct{...} term; nil otherwise.
func coreStruct(t types.Type) *types.Struct {
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		if iface, ok := tp.Constraint().Underlying().(*types.Interface); ok && iface.NumEmbeddeds() == 1 {
			if u, ok := iface.EmbeddedType(0).(*types.Union); ok && u.Len() == 1 {
				t = u.Term(0).Type()
			}
		}
	}
	st, _ := t.Underlying().(*types.Struct)
	return st
}

// envField reports whether e (an assignment target or address operand) selects, through
// slice indexing if need be, a field of a SpawnEnvelope, directly or promoted.
func envField(info *types.Info, e ast.Expr) bool {
	for {
		switch y := ast.Unparen(e).(type) {
		case *ast.IndexExpr:
			e = y.X
			continue
		case *ast.SelectorExpr:
			s := info.Selections[y]
			if s == nil || s.Kind() != types.FieldVal {
				return false
			}
			t := s.Recv()
			for _, i := range s.Index() {
				if isSpawnEnv(t) {
					return true
				}
				if p, ok := types.Unalias(t).(*types.Pointer); ok {
					t = p.Elem()
				}
				st, ok := t.Underlying().(*types.Struct)
				if !ok {
					return false
				}
				t = st.Field(i).Type()
			}
		}
		return false
	}
}

func TestArchSpawnEnvelopeForging_RealTree(t *testing.T) {
	root := archModuleRoot(t)
	v, err := ScanSpawnEnvelopeForging(root, envForgingAllowed)
	expect(t, "real tree", v, err, "")
	// Non-vacuity: with the allowance off the scan sees the projection and the conformance suite.
	v, err = ScanSpawnEnvelopeForging(root, func(string) bool { return false })
	for _, want := range []string{"internal/providers/agents/project.go", "providers/agents/conformance/cases_envelope.go"} {
		if err != nil || !slices.ContainsFunc(v, func(x AgentSpawnViolation) bool { return x.File == want }) {
			t.Fatalf("an unallowed scan of the real tree found %v (error %v), want %s", agentWhere(v), err, want)
		}
	}
}

func TestArchSpawnEnvelopeForging_SeededViolationRed(t *testing.T) {
	const bad = "internal/scratch/bad.go"
	src := func(body string) string {
		return "package scratch\n\nimport \"" + cascadeModulePath + "/pkg/provider\"\n\n" + body + "\n"
	}
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"value":               {map[string]string{bad: src(`var E = provider.SpawnEnvelope{Digest: "x"}`)}, bad},
		"pointer":             {map[string]string{bad: src(`var E = &provider.SpawnEnvelope{Digest: "x"}`)}, bad},
		"alias":               {map[string]string{bad: src(`type E = provider.SpawnEnvelope`)}, bad},
		"field write":         {map[string]string{bad: src(`func F(s *provider.AgentJobSpec) { s.Envelope.Digest = "x" }`)}, bad},
		"element write":       {map[string]string{bad: src(`func F(s *provider.AgentJobSpec) { s.Envelope.Roots[0] = "/" }`)}, bad},
		"address of field":    {map[string]string{bad: src(`func F(s *provider.AgentJobSpec) *[]string { return &s.Envelope.Roots }`)}, bad},
		"new":                 {map[string]string{bad: src(`var E = new(provider.SpawnEnvelope)`)}, bad},
		"defined type":        {map[string]string{bad: src("type E provider.SpawnEnvelope\n\nvar X = E{Digest: \"x\"}")}, bad},
		"conversion":          {map[string]string{bad: src("type E provider.SpawnEnvelope\n\nvar e E\n\nvar X = provider.SpawnEnvelope(e)")}, bad},
		"generic literal":     {map[string]string{bad: src("func mk[T ~struct {\n\tDigest, Principal string\n\tRoots, Tools, Commands, Egress []string\n\tSensitivity, ExpiresAt int\n}]() T { return T{Digest: \"x\"} }\n\nvar X = mk[provider.SpawnEnvelope]()")}, bad},
		"elided in a sibling": {map[string]string{"internal/scratch/a.go": src(`type Envs []provider.SpawnEnvelope`), bad: "package scratch\n\nvar X = Envs{{Digest: \"x\"}}\n"}, bad},
		"alias in the shim":   {map[string]string{"internal/providers/agents/alias.go": strings.Replace(src(`type Env = provider.SpawnEnvelope`), "scratch", "agents", 1)}, "internal/providers/agents/alias.go"},
		"shim subpackage":     {map[string]string{"internal/providers/agents/sub/bad.go": src(`var E = provider.SpawnEnvelope{Digest: "x"}`)}, "internal/providers/agents/sub/bad.go"},
	} {
		v, err := ScanSpawnEnvelopeForging(spawnFixture(t, c.files), envForgingAllowed)
		expect(t, name, v, err, c.want)
	}
	// The projection package may build one; reading a field is always legal.
	inShim := strings.Replace(src(`var E = provider.SpawnEnvelope{Digest: "x"}`), "scratch", "agents", 1)
	read := src(`func F(s provider.AgentJobSpec) string { return s.Envelope.Digest }`)
	v, err := ScanSpawnEnvelopeForging(spawnFixture(t, map[string]string{"internal/providers/agents/e.go": inShim, bad: read}), envForgingAllowed)
	expect(t, "allowed build and a plain read", v, err, "")
}
