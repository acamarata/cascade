// Purpose: the source sweep proving the process trust fields are assigned
// only by grantProcessTrust's callers (and UpdatePlugin's copy) anywhere in
// non-test internal/ and cmd/, that the processTrust value commitUpdate saves
// is built only by keptTrust and attestedUpdateTrust, and that process
// authority is read only through EffectiveGrants in internal/plugins/** and
// cmd/cascade. A seeded source with forged writes proves the sweep reports
// what it is meant to.
// SPORT: internal/plugins process trust grant tests (P1-PLG-10).
package plugins

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The allow tables (trustWriters, trustBuilders, grantsReaders) live in
// install_middleware_test.go, beside the middleware whose params the grant
// reads; this file keeps under the line cap.

var (
	trustFieldNames = map[string]bool{"TrustTier": true, "ExecDigest": true, "TrustGrants": true}
	builtFieldNames = map[string]bool{"Tier": true, "Digest": true, "Bound": true}
)

// sweptFile is one parsed file and what it can name.
type sweptFile struct {
	rel        string
	pluginsPkg bool            // package internal/plugins itself
	alias      string          // its name for internal/plugins, if imported
	imports    map[string]bool // every package name it imports
}

// tierSite reports whether a TrustTier write here can be PluginMetadata's.
func (s sweptFile) tierSite() bool { return s.pluginsPkg || s.alias != "" }

// isType reports whether a composite literal type is name (PluginMetadata or
// processTrust), declared in internal/plugins.
func (s sweptFile) isType(e ast.Expr, name string) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return s.pluginsPkg && x.Name == name
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && s.alias != "" && id.Name == s.alias && x.Sel.Name == name
	}
	return false
}

// trustSweep returns "rel:func:field" for every trust-field write or forged
// processTrust in src and "rel:func" for every .Grants read.
func trustSweep(rel string, src []byte, pluginsPkg bool) (writes, reads []string) {
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
	if err != nil {
		return []string{rel + ": unparsable: " + err.Error()}, nil
	}
	s := sweptFile{rel: rel, pluginsPkg: pluginsPkg, imports: map[string]bool{}}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		s.imports[name] = true
		if path == "github.com/acamarata/cascade/internal/plugins" {
			s.alias = name
		}
	}
	for _, decl := range f.Decls {
		w, r := s.sweepDecl(decl)
		writes, reads = append(writes, w...), append(reads, r...)
	}
	return writes, reads
}

// sweepDecl reports one top-level declaration's writes and reads.
func (s sweptFile) sweepDecl(decl ast.Decl) (writes, reads []string) {
	fn := ""
	if fd, ok := decl.(*ast.FuncDecl); ok {
		fn = fd.Name.Name
	}
	site := s.rel + ":" + fn
	add := func(field string) { writes = append(writes, site+":"+field) }
	lhs, metadataElems := map[ast.Expr]bool{}, map[ast.Expr]bool{}
	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, e := range n.Lhs {
				lhs[e] = true
				s.fieldWrite(e, add)
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				s.fieldWrite(n.X, add)
			}
		case *ast.CallExpr:
			if s.isType(n.Fun, "processTrust") {
				add("processTrust conversion")
			}
		case *ast.CompositeLit:
			s.literal(n, metadataElems, add)
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); n.Sel.Name == "Grants" && !lhs[n] && (!ok || !s.imports[id.Name]) {
				reads = append(reads, site)
			}
		}
		return true
	})
	return writes, reads
}

// fieldWrite reports an assignment to a trust field, or (in package
// internal/plugins) to a processTrust field.
func (s sweptFile) fieldWrite(e ast.Expr, add func(string)) {
	field := selectedField(e)
	switch {
	case trustFieldNames[field] && (field != "TrustTier" || s.tierSite()):
		add(field)
	case builtFieldNames[field] && s.pluginsPkg:
		add("processTrust." + field)
	}
}

// literal reports trust fields set by a PluginMetadata literal (keyed or
// unkeyed, including elided element literals) and any non-empty processTrust
// literal.
func (s sweptFile) literal(n *ast.CompositeLit, metadataElems map[ast.Expr]bool, add func(string)) {
	if elem := containerElem(n.Type); elem != nil && s.isType(elem, "PluginMetadata") {
		for _, el := range n.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			metadataElems[el] = true
		}
	}
	metadata := s.isType(n.Type, "PluginMetadata") || metadataElems[n]
	if s.isType(n.Type, "processTrust") && len(n.Elts) > 0 {
		add("processTrust literal")
	}
	for _, el := range n.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			if metadata {
				add("unkeyed PluginMetadata")
			}
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && trustFieldNames[key.Name] && (key.Name != "TrustTier" || n.Type == nil || metadata) {
			add(key.Name)
		}
	}
}

// containerElem is the element type of a slice, array or map literal type.
func containerElem(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.ArrayType:
		return x.Elt
	case *ast.MapType:
		return x.Value
	}
	return nil
}

// selectedField is the field e assigns, through any index, paren or
// dereference, or "".
func selectedField(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.IndexExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel.Name
		default:
			return ""
		}
	}
}

// authorityReadScope reports whether rel is a file whose .Grants reads the
// sweep checks: internal/plugins and its subpackages, and cmd/cascade.
func authorityReadScope(rel string) bool {
	return strings.HasPrefix(rel, "internal/plugins/") || strings.HasPrefix(rel, "cmd/cascade/")
}

// sweepTree runs trustSweep over every non-test .go file under internal/ and
// cmd/, then over the seeded input. It also returns how many files the read
// scope covered.
func sweepTree(t *testing.T) (writes, reads []string, readFiles int) {
	t.Helper()
	root := filepath.Join("..", "..")
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.Contains(rel, "/testdata/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			w, r := trustSweep(rel, src, filepath.ToSlash(filepath.Dir(rel)) == "internal/plugins")
			writes = append(writes, w...)
			if authorityReadScope(rel) {
				reads, readFiles = append(reads, r...), readFiles+1
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	w, r := trustSweep("internal/plugins/trust_grant_sweep_seed.go", []byte(trustSweepSeed), true)
	return append(writes, w...), append(reads, r...), readFiles
}

// trustSweepSeed is the stub-passing input: every way to forge a trusted
// record outside the grant, and a launch reading Grants directly.
const trustSweepSeed = `package plugins
func granted(rec PluginMetadata) { rec.TrustTier = TrustTrusted }
func forged() PluginMetadata { return PluginMetadata{Name: "x", TrustTier: TrustTrusted, ExecDigest: "00"} }
func unkeyed() []PluginMetadata { return []PluginMetadata{{"x", "1", true, "process", "", nil, 0, TrustTrusted, nil, "00"}} }
func forgedTrust() processTrust { return processTrust{Tier: TrustTrusted, Digest: "00"} }
func fieldTrust(p *processTrust) { p.Tier = TrustTrusted }
func launch(rec PluginMetadata) []string { return rec.Grants }
`

// sweepFindings classifies the sweep's output: allowed writes by site, and
// every forbidden write or read.
func sweepFindings(writes, reads []string) map[string]bool {
	seen := map[string]bool{}
	for _, w := range writes {
		site := w[:strings.LastIndex(w, ":")]
		field := w[len(site)+1:]
		allowed := strings.Contains(trustWriters[site], field) && trustFieldNames[field]
		if strings.HasPrefix(field, "processTrust") {
			allowed = trustBuilders[site]
		}
		if allowed {
			seen[w] = true
		} else {
			seen["forbidden "+w] = true
		}
	}
	for _, r := range reads {
		if !grantsReaders[r] {
			seen["forbidden read "+r] = true
		}
	}
	return seen
}

func TestTrustTierWrittenOnlyByGrant(t *testing.T) {
	writes, reads, readFiles := sweepTree(t)
	if readFiles < 100 {
		t.Fatalf("the authority-read scope covered %d files; it must cover internal/plugins/** and cmd/cascade", readFiles)
	}
	seen := sweepFindings(writes, reads)
	seed := "internal/plugins/trust_grant_sweep_seed.go:"
	want := []string{seed + "granted:TrustTier", "forbidden " + seed + "forged:TrustTier", "forbidden " + seed + "forged:ExecDigest",
		"forbidden " + seed + "unkeyed:unkeyed PluginMetadata", "forbidden " + seed + "forgedTrust:processTrust literal",
		"forbidden " + seed + "fieldTrust:processTrust.Tier", "forbidden read " + seed + "launch",
		"internal/plugins/lifecycle_update.go:keptTrust:processTrust literal",
		"internal/plugins/update_rpc.go:attestedUpdateTrust:processTrust literal"}
	for _, field := range []string{"TrustTier", "ExecDigest", "TrustGrants"} {
		want = append(want, "internal/plugins/dispatch.go:ProvisionElevated:"+field, "internal/plugins/lifecycle_update.go:commitUpdate:"+field)
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("the sweep did not report %s", w)
		}
		delete(seen, w)
	}
	for finding := range seen {
		if strings.HasPrefix(finding, "forbidden") {
			t.Errorf("%s: trust is written only by the grant's callers and process authority is read only through EffectiveGrants", finding)
		}
	}
}
