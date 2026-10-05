// One-sensitivity-type arch gate (P1-SEC-19, R126, R136): pkg/provider's two
// tier types stay the only string/integer-kind declarations of their kind; an
// alias passes only when it reaches one. Rules and the recorded iota-int gap
// (DEBT-PROC-43) are in docs/reference/sensitivity.md.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// sensNameRule (type names) and sensWords (const values: every vocabulary's tier spellings) are the two rules.
	sensNameRule = regexp.MustCompile(`(?i)(sensitiv|dataclass|provenance|trustlevel|trusttag)`)
	sensWords    = map[string]bool{"local-only": true, "restricted": true, "internal": true, "public": true, "confidential": true, "secret": true, "trusted": true, "untrusted-source": true, "normal": true}
	// sensCanonical are the two declarations the gate keeps unique.
	sensCanonical = map[string]bool{"pkg/provider.SensitivityTier": true, "pkg/provider.Provenance": true}
	// sensPredeclared matches every predeclared type; sensInKind its string and integer kinds.
	sensPredeclared = regexp.MustCompile(`^(string|u?int(8|16|32|64|ptr)?|byte|rune|error|any|comparable|bool|float(32|64)|complex(64|128))$`)
	sensInKind      = regexp.MustCompile(`^(string|u?int(8|16|32|64|ptr)?|byte|rune)$`)
	// sensTierFields: nodes literals whose unset tier places as restricted -> the keyed field carrying it.
	sensTierFields = map[string]string{"internal/nodes.Requirement": "Sensitivity", "internal/nodes.ShipRequest": "Sensitivity", "internal/nodes.RequeueRequest": "Requirement"}
)

// sensExemptions: another concept or ticket, with the reason (memory.Provenance, a struct, and process TrustTier need none).
var sensExemptions = map[string]string{
	"pkg/provider.DataClass":            "owner P1-SEC-34: agent data class; P1-AGT-01 WIP file",
	"internal/evidence.DataClass":       "owner P1-SEC-34: vocabulary 2 (public..secret), persisted codec",
	"internal/jobs.DataClass":           "owner P1-SEC-34: vocabulary 2, persisted job rows",
	"internal/fleet/capacity.DataClass": "owner P1-SEC-34: vocabulary 2, capacity requests",
	"internal/policy.DataClass":         "owner P1-SEC-34: vocabulary 2 inside the signed approval record",
	"internal/repo.SensitiveClass":      "a file-content class {auth, secret, schema}, not a data tier (P1-BF-R126)",
	"internal/nodes.Gate":               "placement gate names (trust.go), not a data tier (P1-BF-R126)",
}

// sensParse parses every non-test .go file under root's five source roots.
func sensParse(t *testing.T, root string) []sensSpec {
	var out []sensSpec
	fset := token.NewFileSet()
	for _, top := range []string{"cmd", "internal", "pkg", "plugins", "providers"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err == nil && d.IsDir() && archIsSkippedDir(d.Name()):
				return filepath.SkipDir
			case err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil // err: an absent root, as fixtures carry only some roots
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Fatalf("sensitivity gate: parse %s: %v", path, perr)
			}
			out = append(out, sensSpec{dir: filepath.ToSlash(strings.TrimPrefix(filepath.Dir(path), root+string(filepath.Separator))), file: f})
			return nil
		})
	}
	return out
}

// sensImportDir maps a file-local import name to its module-relative dir.
func sensImportDir(f *ast.File, name string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		local := p[strings.LastIndex(p, "/")+1:]
		if (imp.Name == nil && local == name || imp.Name != nil && imp.Name.Name == name) && strings.HasPrefix(p, cascadeModulePath+"/") {
			return strings.TrimPrefix(p, cascadeModulePath+"/")
		}
	}
	return ""
}

// sensSpec is a parsed file and its slash dir, plus one TypeSpec once indexed.
type sensSpec struct {
	spec *ast.TypeSpec
	dir  string
	file *ast.File
}

// sensTypeKey is the "dir.Name" a type expression names, or "" for a predeclared, composite or non-module type.
func sensTypeKey(dir string, f *ast.File, expr ast.Expr) string {
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		if !sensPredeclared.MatchString(e.Name) {
			return dir + "." + e.Name
		}
	case *ast.SelectorExpr:
		if d := sensImportDir(f, identName(e.X)); d != "" {
			return d + "." + e.Sel.Name
		}
	}
	return ""
}

// sensKind: in is a string/integer kind (unresolvable named types count); canon the canonical type expr is defined over.
func sensKind(idx map[string]sensSpec, dir string, f *ast.File, expr ast.Expr, depth int) (in bool, canon string) {
	expr = ast.Unparen(expr)
	key := sensTypeKey(dir, f, expr)
	s, known := idx[key]
	_, named := expr.(*ast.SelectorExpr)
	switch id, isIdent := expr.(*ast.Ident); {
	case isIdent && key == "":
		return sensInKind.MatchString(id.Name), ""
	case sensCanonical[key]:
		return true, key
	case !known || depth > 8: // a non-module or unknown named type counts
		return named || key != "", ""
	}
	return sensKind(idx, s.dir, s.file, s.spec.Type, depth+1)
}

// sensResolve follows key's alias chain to the last declared type reached
// (key itself for a non-alias) and reports whether that is a canonical type.
func sensResolve(idx map[string]sensSpec, key string, depth int) (target string, canon bool) {
	s, ok := idx[key]
	if sensCanonical[key] || !ok || !s.spec.Assign.IsValid() || depth > 8 {
		return key, sensCanonical[key]
	}
	next := sensTypeKey(s.dir, s.file, s.spec.Type)
	if _, known := idx[next]; known || sensCanonical[next] {
		return sensResolve(idx, next, depth+1)
	}
	return key, false
}

// sensFindings returns "dir.Name (rule)" for every violating declaration;
// withExempt keeps the exempt ones.
func sensFindings(files []sensSpec, withExempt bool) []string {
	idx, words := map[string]sensSpec{}, map[string]map[string]bool{}
	for pass := 0; pass < 2; pass++ { // types first, so consts can resolve aliases
		for _, sf := range files {
			for _, decl := range sf.file.Decls {
				gd, _ := decl.(*ast.GenDecl)
				for i := 0; gd != nil && i < len(gd.Specs); i++ {
					if s, ok := gd.Specs[i].(*ast.TypeSpec); ok && pass == 0 {
						idx[sf.dir+"."+s.Name.Name], words[sf.dir+"."+s.Name.Name] = sensSpec{spec: s, dir: sf.dir, file: sf.file}, map[string]bool{}
					} else if v, ok := gd.Specs[i].(*ast.ValueSpec); ok && pass == 1 && gd.Tok == token.CONST {
						sensCountWords(words, idx, sf, v)
					}
				}
			}
		}
	}
	var out []string
	for key, s := range idx {
		in, canon := sensKind(idx, s.dir, s.file, s.spec.Type, 0)
		switch _, toCanon := sensResolve(idx, key, 0); {
		case toCanon || !in || (!withExempt && sensExemptions[key] != ""):
		case sensNameRule.MatchString(s.spec.Name.Name):
			out = append(out, key+" (name)")
		case len(words[key]) >= 2:
			out = append(out, key+" (const values)")
		case canon != "" && !s.spec.Assign.IsValid(): // an alias's target answers for it
			out = append(out, key+" (defined over "+canon+")")
		}
	}
	return out
}

// sensCountWords records the sensWords a const gives its type, typed or in
// conversion form (`A = T("x")`), under the alias-resolved target.
func sensCountWords(words map[string]map[string]bool, idx map[string]sensSpec, sf sensSpec, v *ast.ValueSpec) {
	for _, val := range v.Values {
		typ := v.Type
		if call, ok := val.(*ast.CallExpr); ok && len(call.Args) == 1 {
			typ, val = call.Fun, call.Args[0]
		}
		key, _ := sensResolve(idx, sensTypeKey(sf.dir, sf.file, typ), 0)
		if w := stringLit(val); w != nil && sensWords[*w] && words[key] != nil {
			words[key][*w] = true
		}
	}
}

// stringLit returns a string literal's value, or nil for anything else.
func stringLit(n ast.Node) *string {
	if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		v, _ := strconv.Unquote(lit.Value)
		return &v
	}
	return nil
}

// sensParseSiteDefaults: does fn at path call provider.ParseSensitivityTier,
// and which local defaults ("" or tier-name literals) does its body keep.
func sensParseSiteDefaults(t *testing.T, path, fn string) (calls bool, defaults []string) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("sensitivity gate: parse %s: %v", path, err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn && fd.Body != nil {
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, _ := n.(*ast.SelectorExpr)
				calls = calls || sel != nil && sel.Sel.Name == "ParseSensitivityTier" && sensImportDir(f, identName(sel.X)) == "pkg/provider"
				if v := stringLit(n); v != nil && (*v == "" || sensWords[*v]) {
					defaults = append(defaults, strconv.Quote(*v))
				}
				return true
			})
		}
	}
	return calls, defaults
}

// identName returns an identifier's name, or "" for any other expression.
func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// sensRequirementGaps lists sensTierFields literals that leave the tier unset.
func sensRequirementGaps(files []sensSpec) []string {
	var out []string
	for _, sf := range files {
		ast.Inspect(sf.file, func(n ast.Node) bool {
			cl, _ := n.(*ast.CompositeLit)
			if cl == nil {
				return true
			}
			key := sensTypeKey(sf.dir, sf.file, cl.Type)
			field, ok := sensTierFields[key]
			for i := 0; ok && i < len(cl.Elts); i++ {
				kv, keyed := cl.Elts[i].(*ast.KeyValueExpr)
				ok = keyed && identName(kv.Key) != field
			}
			if ok {
				out = append(out, sf.dir+": "+key+" literal without "+field)
			}
			return true
		})
	}
	return out
}

// TestArchOneSensitivityType_RealTreeGreen: no un-exempt declaration; both former parse sites call the closed parser, no default.
func TestArchOneSensitivityType_RealTreeGreen(t *testing.T) {
	root := archModuleRoot(t)
	if v := sensFindings(sensParse(t, root), false); len(v) > 0 {
		t.Fatalf("sensitivity gate: alias provider.SensitivityTier or provider.Provenance instead of declaring:\n  %s", strings.Join(v, "\n  "))
	}
	for path, fn := range map[string]string{"cmd/cascade/run.go": "validateSensitivity", "internal/daemon/conductor_execute_params.go": "parseSensitivityTier"} {
		if calls, defaults := sensParseSiteDefaults(t, filepath.Join(root, path), fn); !calls || len(defaults) > 0 {
			t.Errorf("%s %s: calls ParseSensitivityTier=%v, local defaults %v; want the closed parser and none", path, fn, calls, defaults)
		}
	}
}

// TestArchOneSensitivityType_SeededViolationRed: each want.tsv fixture fires exactly its finding; a kept default is caught.
func TestArchOneSensitivityType_SeededViolationRed(t *testing.T) {
	base := filepath.Join("testdata", "seeded-violations", "sensitivity-type")
	raw, err := os.ReadFile(filepath.Join(base, "want.tsv"))
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if err != nil || len(rows) != 12 {
		t.Fatalf("want.tsv: %d rows, %v; want 12", len(rows), err)
	}
	for _, row := range rows {
		fixture, want, _ := strings.Cut(row, "\t")
		if got := strings.Join(sensFindings(sensParse(t, archMaterialize(t, filepath.Join(base, fixture))), false), "\n"); got != want {
			t.Errorf("fixture %s: findings %q, want exactly %q", fixture, got, want)
		}
	}
	calls, defaults := sensParseSiteDefaults(t, filepath.Join(base, "parse-site-default", "run.go"), "validateSensitivity")
	if !calls || len(defaults) == 0 {
		t.Errorf("parse-site-default: calls=%v defaults=%v; want the kept default caught", calls, defaults)
	}
}

// TestArchSensitivityExemptionsNotStale: every exemption still matches.
func TestArchSensitivityExemptionsNotStale(t *testing.T) {
	found := map[string]bool{}
	for _, f := range sensFindings(sensParse(t, archModuleRoot(t)), true) {
		found[f[:strings.Index(f, " ")]] = true
	}
	for key, reason := range sensExemptions {
		if reason == "" || !found[key] {
			t.Errorf("exemption %s (%q) matches no flagged declaration or has no reason; delete it", key, reason)
		}
	}
}

// TestRequirementLiteralsSetSensitivity: no placement, ship or requeue literal relies on the zero tier; seeds are caught.
func TestRequirementLiteralsSetSensitivity(t *testing.T) {
	if gaps := sensRequirementGaps(sensParse(t, archModuleRoot(t))); len(gaps) > 0 {
		t.Fatalf("set the tier explicitly:\n  %s", strings.Join(gaps, "\n  "))
	}
	dir := filepath.Join("testdata", "seeded-violations", "sensitivity-type", "requirement")
	want, err := os.ReadFile(filepath.Join(dir, "gaps.txt"))
	if got := strings.Join(sensRequirementGaps(sensParse(t, archMaterialize(t, dir))), "\n"); err != nil || got != strings.TrimSpace(string(want)) {
		t.Errorf("seeded literals: gaps %q, want exactly %q (%v)", got, want, err)
	}
}
