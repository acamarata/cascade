package build

// Purpose: the AST walk behind UnmodeledGuardedKeys (guardedconfiggate.go):
//   visit roots each function and package-level declaration, walk records
//   parents, references, local definitions and guarded reads, pathOf resolves
//   the config path an expression holds, and site decides whether a read's
//   value only goes somewhere safe (a nested read, a nil check, a local or a
//   handoff to a module function).
// Inputs: one type-checked file at a time (guardedGate.info, fset, rel).
// Outputs: guardedGate.sites, calls and roots. No I/O.
// Constraints: a computed key is "?" and an unresolvable path is nil, so an
//   unknown shape never matches a family as modeled.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

// visit walks each function and each package-level declaration (a func
// literal in a var) once; a GuardedFamily literal roots its Section.
func (g *guardedGate) visit(n ast.Node) bool {
	if _, ok := n.(*ast.File); ok || n == nil {
		return ok
	}
	g.fn, g.parent, g.defs, g.params = g.rel+":init", map[ast.Node]ast.Node{}, map[types.Object][]ast.Expr{}, map[types.Object]string{}
	if fd, ok := n.(*ast.FuncDecl); ok {
		g.fn = funcName(g.info.Defs[fd.Name])
		i := 0
		for _, field := range fd.Type.Params.List {
			for _, name := range field.Names {
				if obj := g.info.Defs[name]; obj != nil && isTree(obj.Type()) {
					g.params[obj] = fmt.Sprintf("%s#%d", g.fn, i)
					g.treeArg[g.params[obj]] = true
				}
				i++
			}
			i += max(0, 1-len(field.Names)) // an unnamed parameter
		}
	}
	g.calls[g.fn] = append(g.calls[g.fn], g.fn) // every walked function is a key
	g.walk(n)
	return false
}

func (g *guardedGate) noteFamily(lit *ast.CompositeLit) {
	// A GuardedFamily literal roots its Section's reads at its Extract; a
	// non-constant Section or a function literal roots nothing (reads fail).
	var section, extract string
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok && fmt.Sprint(kv.Key) == "Section" {
			section = constString(g.info, kv.Value)
		} else if ok && fmt.Sprint(kv.Key) == "Extract" {
			extract = g.calleeName(kv.Value)
		}
	}
	g.roots[section] = append(g.roots[section], extract)
}

func funcName(obj types.Object) string {
	if fn, ok := obj.(*types.Func); ok {
		return fn.Origin().FullName()
	}
	return ""
}

func (g *guardedGate) calleeName(e ast.Expr) string {
	if sel, ok := ast.Unparen(e).(*ast.SelectorExpr); ok {
		e = sel.Sel
	}
	return funcName(g.info.Uses[identOf(ast.Unparen(e))])
}

func identOf(e ast.Expr) *ast.Ident { id, _ := e.(*ast.Ident); return id }

func isRuntimeNamed(t types.Type, name string) bool {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == name && n.Obj().Pkg() != nil && strings.HasSuffix(n.Obj().Pkg().Path(), "internal/runtime")
}

func constString(info *types.Info, e ast.Expr) string {
	if tv, ok := info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value)
	}
	return ""
}

func isTree(t types.Type) bool {
	if t == nil { // a package with type errors: load reports it
		return false
	}
	m, ok := t.Underlying().(*types.Map)
	return ok && types.Identical(m.Key(), types.Typ[types.String]) && types.Identical(m.Elem(), types.NewInterfaceType(nil, nil))
}

// walk records fd's parents, references, locals and reads in one pre-order pass.
func (g *guardedGate) walk(fd ast.Node) {
	ast.Inspect(fd, g.define) // every local's values first, so a later write counts
	var stack []ast.Node
	ast.Inspect(fd, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			g.parent[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.Ident: // a reference, or a use of a local holding a section
			if fn := funcName(g.info.Uses[x]); fn != "" {
				g.calls[g.fn] = append(g.calls[g.fn], fn)
			} else if a, ok := g.parent[x].(*ast.AssignStmt); g.info.Uses[x] != nil && (!ok || !slices.Contains(a.Lhs, ast.Expr(x))) {
				g.site(x, g.pathOf(x, 0))
				if g.isExtra(x) { // a local holding the whole tree
					g.site(x, []string{"*"})
				} else if k, ok := g.params[g.info.Uses[x]]; ok { // a tree parameter
					g.param = k
					g.site(x, []string{"*"})
					g.param = ""
				}
			}
		case *ast.SelectorExpr: // the whole tree goes only where its reads stay gated
			if g.isExtra(x) {
				g.site(x, []string{"*"})
			}
		case *ast.CompositeLit:
			if isRuntimeNamed(g.info.TypeOf(x), "GuardedFamily") {
				g.noteFamily(x)
			}
		case *ast.RangeStmt:
			if g.isExtra(x.X) {
				g.site(x.X, []string{"*"})
			}
		case *ast.IndexExpr:
			if k := g.keyOf(x.Index); k != "" && isTree(g.info.TypeOf(x.X)) {
				g.site(x, append(g.pathOf(x.X, 0), k))
			}
		case *ast.CallExpr:
			if a, keys := g.helper(x); a != nil {
				g.site(x, append(g.pathOf(a, 0), keys...))
			}
		}
		return true
	})
}

// pathOf resolves the config path e holds (nil: the root, or unknown).
func (g *guardedGate) pathOf(e ast.Expr, d int) []string {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return g.pathOf(x.X, d+1)
	case *ast.TypeAssertExpr:
		return g.pathOf(x.X, d+1)
	case *ast.CallExpr:
		if a, keys := g.helper(x); a != nil {
			return append(slices.Clone(g.pathOf(a, d+1)), keys...)
		}
	case *ast.IndexExpr:
		k := g.keyOf(x.Index)
		if k == "" {
			k = "?" // a computed key
		}
		return append(slices.Clone(g.pathOf(x.X, d+1)), k)
	case *ast.Ident:
		vals := g.defs[g.info.ObjectOf(x)]
		if len(vals) == 0 || d > 8 {
			return nil
		}
		p := g.pathOf(vals[0], d+1)
		for _, v := range vals[1:] {
			if !slices.Equal(g.pathOf(v, d+1), p) {
				return nil
			}
		}
		return p
	}
	return nil
}

// site records a guarded read and where its value goes: a call (or a call
// wrapping it), a nested read, a nil check or a local (whose uses are reads).
func (g *guardedGate) site(n ast.Expr, path []string) {
	key := strings.Join(path, ".")
	for _, family := range guardedFamilies {
		if f := strings.Split(family, "."); len(path) >= len(f) && (!guardedParents[family] || len(path) == 1) && slices.Equal(path[:len(f)], f) {
			pos := fmt.Sprintf("%s:%d", g.rel, g.fset.Position(n.Pos()).Line)
			site := guardedSite{pos: pos, fn: g.fn, family: family, key: key, param: g.param}
			e := ast.Node(n)
			for isWrapper(g.parent[e]) {
				e = g.parent[e]
			}
			switch p := g.parent[e].(type) {
			case *ast.IndexExpr: // the whole tree read by a computed key is not modeled
				site.safe = p.X == e && (key != "*" || g.keyOf(p.Index) != "")
				site.computed = p.X == e && !site.safe
			case *ast.BinaryExpr: // a nil check only
				site.safe = (p.Op == token.EQL || p.Op == token.NEQ) && (fmt.Sprint(p.X) == "nil" || fmt.Sprint(p.Y) == "nil")
			case *ast.AssignStmt: // into _ or a local that holds exactly this path
				i := slices.Index(p.Rhs, e.(ast.Expr))
				site.safe = i >= 0 && identOf(p.Lhs[i]) != nil && (identOf(p.Lhs[i]).Name == "_" || slices.Equal(g.pathOf(p.Lhs[i], 0), path) ||
					key == "*" && g.isExtra(p.Lhs[i]))
			case *ast.CallExpr:
				if site.safe = p.Fun != e; fmt.Sprint(p.Fun) == "len" {
					break
				}
				a, _ := g.helper(p)
				site.helper, site.arg = key == "*" && a == e, slices.Index(p.Args, e.(ast.Expr))
				if sel, ok := ast.Unparen(p.Fun).(*ast.SelectorExpr); ok && g.info.Selections[sel] != nil && g.info.Selections[sel].Kind() == types.MethodExpr {
					site.arg-- // T.M(recv, args...): the receiver is argument 0
				}
				site.handoff = []string{g.calleeName(p.Fun)}
				if outer, ok := g.parent[p].(*ast.CallExpr); ok {
					site.handoff = append(site.handoff, g.calleeName(outer.Fun))
				}
			}
			g.sites[fmt.Sprintf("%s#%d %s", g.rel, n.Pos(), key)] = site // per node: a later use never hides one
			return
		}
	}
}

func isWrapper(n ast.Node) bool {
	_, p := n.(*ast.ParenExpr)
	_, a := n.(*ast.TypeAssertExpr)
	return p || a
}

var guardedParents = map[string]bool{"agents": true, "retrieval": true}

// helper finds a call passing a config map then constant names: a section helper.
func (g *guardedGate) helper(x *ast.CallExpr) (ast.Expr, []string) {
	for i, a := range x.Args {
		var keys []string
		for j := i + 1; j < len(x.Args) && g.keyOf(x.Args[j]) != ""; j++ {
			keys = append(keys, g.keyOf(x.Args[j]))
		}
		if t := g.info.TypeOf(a); t != nil && isTree(t) && len(keys) > 0 {
			return a, keys
		}
	}
	return nil, nil
}

// keyOf is a constant string, or the constant a local was given.
func (g *guardedGate) keyOf(e ast.Expr) string {
	if vals := g.defs[g.info.ObjectOf(identOf(e))]; len(vals) == 1 {
		return constString(g.info, vals[0])
	}
	return constString(g.info, e)
}

// define records a local's assigned values: one per name (or the value of
// v, ok := e); a range clause or an address taken (&k) is an unknown value.
func (g *guardedGate) define(n ast.Node) bool {
	unknown := func(e ast.Expr) {
		if obj := g.info.ObjectOf(identOf(e)); obj != nil {
			g.defs[obj] = append(g.defs[obj], nil)
		}
	}
	switch x := n.(type) {
	case *ast.AssignStmt:
		for i, l := range x.Lhs {
			if obj := g.info.ObjectOf(identOf(l)); obj != nil && (len(x.Rhs) == len(x.Lhs) || i == 0) {
				g.defs[obj] = append(g.defs[obj], x.Rhs[i])
			}
		}
	case *ast.RangeStmt:
		unknown(x.Key)
		unknown(x.Value)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			unknown(x.X)
		}
	}
	return true
}

// isExtra reports runtime.Config.Extra, directly or through locals each
// assigned once (a chain longer than 8, or a cycle, is not the tree).
func (g *guardedGate) isExtra(e ast.Expr) bool {
	for d := 0; d < 8; d++ {
		vals := g.defs[g.info.ObjectOf(identOf(e))]
		if len(vals) != 1 {
			break
		}
		e = vals[0]
	}
	sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Extra" && isRuntimeNamed(g.info.TypeOf(sel.X), "Config")
}
