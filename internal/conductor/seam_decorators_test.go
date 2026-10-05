// Purpose: the ONE provider-decorator exemption table both ModelProvider
//
//	gates consult (seam_test.go and callgraph_test.go), plus the seeded
//	cases proving it admits nothing else (P1-BF-R103). A call is exempt
//	only inside a method of a listed receiver type, in the listed file,
//	named V (a ModelProvider verb), whose body makes exactly one
//	ModelProvider call, V on the receiver's own inner field (p.inner.V),
//	with the receiver never re-bound or written. The seam allowlist stays
//	empty (R-40.X10); this table describes a forwarder, not allowed callers.
package conductor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seamDecorator names one pure-forwarding decorator: the slash-separated
// repo-relative file declaring it and its receiver type.
type seamDecorator struct{ file, recv string }

// seamDecorators is the exemption table. Adding a row is a gate change and
// needs the same review as widening the seam itself.
var seamDecorators = []seamDecorator{
	{file: "internal/providers/dispatch/lane_outcome.go", recv: "laneOutcomeProvider"},
}

const seamDecoratorField = "inner" // the receiver field a forwarder calls through

// seamRecvNames returns the receiver variable and type names of fd, or
// empty strings when fd is not a method with one named receiver.
func seamRecvNames(fd *ast.FuncDecl) (recvName, typeName string) {
	if fd.Recv == nil || len(fd.Recv.List) != 1 || len(fd.Recv.List[0].Names) != 1 {
		return "", ""
	}
	f := fd.Recv.List[0]
	t := f.Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	id, ok := t.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return f.Names[0].Name, id.Name
}

// seamProviderCalls lists ModelProvider verb calls in body (closures too).
func seamProviderCalls(body *ast.BlockStmt, vars map[string]bool) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !seamVerbs[sel.Sel.Name] {
			return true
		}
		if name, ok := seamRightmostIdent(sel.X); ok && vars[name] {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// seamIsInnerForward reports whether call is exactly <recvName>.inner.<verb>.
func seamIsInnerForward(call *ast.CallExpr, recvName, verb string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != verb {
		return false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != seamDecoratorField {
		return false
	}
	x, ok := field.X.(*ast.Ident)
	return ok && x.Name == recvName
}

// seamRoot returns the identifier a selector/index/star/paren chain starts at.
func seamRoot(e ast.Expr) string {
	for {
		switch x := ast.Unparen(e).(type) {
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// seamRebinds reports whether body does anything with name except read one
// of its fields (name.f, not called). Hits: any other identifier named name
// (declaration, :=, var, range variable, closure parameter or named result,
// bare use, method call name.m()), any write rooted in name (assignment,
// ++/--, range target, name.inner = x) and any &name... address-of.
func seamRebinds(body *ast.BlockStmt, name string) bool {
	hit, read, called := false, map[*ast.Ident]bool{}, map[*ast.SelectorExpr]bool{}
	root := func(es ...ast.Expr) {
		for _, e := range es {
			hit = hit || (e != nil && seamRoot(e) == name)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.CallExpr:
			if sel, ok := s.Fun.(*ast.SelectorExpr); ok {
				called[sel] = true
			}
		case *ast.SelectorExpr:
			if id, ok := s.X.(*ast.Ident); ok && !called[s] {
				read[id] = true
			}
		case *ast.Ident:
			hit = hit || (s.Name == name && !read[s])
		case *ast.AssignStmt:
			root(s.Lhs...)
		case *ast.IncDecStmt:
			root(s.X)
		case *ast.RangeStmt:
			root(s.Key, s.Value)
		case *ast.UnaryExpr:
			if s.Op == token.AND {
				root(s.X)
			}
		}
		return !hit
	})
	return hit
}

// seamExemptMethods returns each exempt forwarding method in file, mapped
// to its single ModelProvider call. rel is the file's repo-relative path.
func seamExemptMethods(file *ast.File, vars map[string]bool, rel string) map[*ast.FuncDecl]*ast.CallExpr {
	rel = filepath.ToSlash(rel)
	out := map[*ast.FuncDecl]*ast.CallExpr{}
	for _, dec := range seamDecorators {
		if dec.file != rel {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			recvName, typeName := seamRecvNames(fd)
			if typeName != dec.recv {
				continue
			}
			// seamProviderCalls counts only seamVerbs names, so a match
			// on fd's own name means fd is named for a verb.
			calls := seamProviderCalls(fd.Body, vars)
			if len(calls) == 1 && seamIsInnerForward(calls[0], recvName, fd.Name.Name) && !seamRebinds(fd.Body, recvName) {
				out[fd] = calls[0]
			}
		}
	}
	return out
}

// seamExemptCalls is the seam gate's view: the exempt call nodes.
func seamExemptCalls(file *ast.File, vars map[string]bool, rel string) map[*ast.CallExpr]bool {
	out := map[*ast.CallExpr]bool{}
	for _, call := range seamExemptMethods(file, vars, rel) {
		out[call] = true
	}
	return out
}

// seamExemptFuncs is the call-graph gate's view: the exempt callers by name.
func seamExemptFuncs(file *ast.File, vars map[string]bool, rel string) map[string]bool {
	out := map[string]bool{}
	for fd := range seamExemptMethods(file, vars, rel) {
		out[callgraphFuncName(fd)] = true
	}
	return out
}

const seamDecoratorHead = `package dispatch

import provider "github.com/acamarata/cascade/pkg/provider"

type laneOutcomeProvider struct{ inner provider.ModelProvider }
`

const seamDecoratorFile = "internal/providers/dispatch/lane_outcome.go"

func seamForwarders() string {
	var b strings.Builder
	for _, v := range []string{"Chat", "Embed", "Count", "Stream", "Capabilities"} {
		fmt.Fprintf(&b, "\nfunc (p *laneOutcomeProvider) %[1]s(ctx, req any) { p.inner.%[1]s(ctx, req) }\n", v)
	}
	return b.String()
}

// seamGateCounts runs src, presented as repo file rel, through the real
// filter of both gates and returns what each still reports.
func seamGateCounts(t *testing.T, rel, src string) (seam, graph int) {
	t.Helper()
	rel = filepath.FromSlash(rel)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	vars := seamProviderVars(file, seamProviderAlias(file))
	seam = len(seamFindViolations(file, vars, fset, rel))
	path := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	graph = len(callgraphEdgesInPath(token.NewFileSet(), path, rel, map[string]bool{}))
	return seam, graph
}

// TestSeamDecorator_ExemptsTheForwardersOnly is the positive control: the
// five faithful forwarders in the listed file are admitted by both gates.
func TestSeamDecorator_ExemptsTheForwardersOnly(t *testing.T) {
	seam, graph := seamGateCounts(t, seamDecoratorFile, seamDecoratorHead+seamForwarders())
	if seam != 0 || graph != 0 {
		t.Fatalf("faithful forwarders flagged: seam=%d callgraph=%d, want 0 and 0", seam, graph)
	}
}

// seamSeed is one seeded bypass: src presented as repo file rel must still
// yield want findings from each gate.
type seamSeed struct {
	name, rel, src string
	want           int
}

const seamFreeChat = "\nfunc helper(mp provider.ModelProvider, ctx, req any) { mp.Chat(ctx, req) }\n"

func seamChatBody(body string) string {
	return "\nfunc (p *laneOutcomeProvider) Chat(ctx, req any) {\n" + body + "\n}\n"
}

// seamSeedsOutsideTheForwarders are bypasses that are not a forwarding
// method at all, or sit in the wrong file or type.
func seamSeedsOutsideTheForwarders() []seamSeed {
	return []seamSeed{
		{"free function in the listed file", seamDecoratorFile, seamDecoratorHead + seamFreeChat, 1},
		{"free function in another dispatch file", "internal/providers/dispatch/resolver.go", seamDecoratorHead + seamFreeChat, 1},
		{"same receiver in another dispatch file", "internal/providers/dispatch/other.go", seamDecoratorHead + seamForwarders(), 5},
		{"same file name in a sibling directory", "internal/providers/lane_outcome.go", seamDecoratorHead + seamForwarders(), 5},
		{"another receiver type in the listed file", seamDecoratorFile,
			seamDecoratorHead + "\ntype other struct{ inner provider.ModelProvider }\n\nfunc (p *other) Chat(ctx, req any) { p.inner.Chat(ctx, req) }\n", 1},
		{"method not named for a verb", seamDecoratorFile,
			seamDecoratorHead + "\nfunc (p *laneOutcomeProvider) Relay(ctx, req any) { p.inner.Chat(ctx, req) }\n", 1},
	}
}

// seamSeedsInsideTheForwarders are bypasses shaped like a forwarding
// method of the listed type in the listed file but not a pure forward.
func seamSeedsInsideTheForwarders() []seamSeed {
	h, f := seamDecoratorHead, seamDecoratorFile
	return []seamSeed{
		{"Chat calls Embed", f, h + seamChatBody("p.inner.Embed(ctx, req)"), 1},
		{"verb on a non-receiver decorator", f, h + "\nfunc (p *laneOutcomeProvider) Chat(ctx, req any, o *laneOutcomeProvider) { o.inner.Chat(ctx, req) }\n", 1},
		{"verb on a non-receiver ModelProvider", f, h + "\nfunc (p *laneOutcomeProvider) Chat(ctx, req any, mp provider.ModelProvider) { mp.Chat(ctx, req) }\n", 1},
		{"two calls", f, h + seamChatBody("p.inner.Chat(ctx, req)\n\tp.inner.Chat(ctx, req)"), 2},
		{"second call in a closure", f, h + seamChatBody("p.inner.Chat(ctx, req)\n\tfunc() { p.inner.Chat(ctx, req) }()"), 2},
		{"receiver name re-bound", f, h + seamChatBody("p = swapped\n\tp.inner.Chat(ctx, req)"), 1},
		{"closure result shadows the receiver", f, h + seamChatBody(
			"func() (p *laneOutcomeProvider) {\n\t\tdefer func() { p.inner.Chat(ctx, req) }()\n\t\treturn swapped\n\t}()"), 1},
		{"pointer write through &p", f, h + seamChatBody("pp := &p\n\t*pp = swapped\n\tp.inner.Chat(ctx, req)"), 1},
		{"inner field swapped first", f, h + seamChatBody("p.inner = other\n\tp.inner.Chat(ctx, req)"), 1},
		{"pointer write through &p.inner", f, h + seamChatBody("pp := &p.inner\n\t*pp = other\n\tp.inner.Chat(ctx, req)"), 1},
		{"method call on the receiver", f, h + seamChatBody("p.swap(other)\n\tp.inner.Chat(ctx, req)"), 1},
		{"call through a second provider field", f,
			strings.Replace(h, "inner provider", "inner, spare provider", 1) + seamChatBody("p.spare.Chat(ctx, req)"), 1},
	}
}

// TestSeamDecorator_SeededCasesStayCaught proves each way around the
// exemption still trips both gates, with the exact finding count.
func TestSeamDecorator_SeededCasesStayCaught(t *testing.T) {
	seeds := append(seamSeedsOutsideTheForwarders(), seamSeedsInsideTheForwarders()...)
	for _, tc := range seeds {
		t.Run(tc.name, func(t *testing.T) {
			seam, graph := seamGateCounts(t, tc.rel, tc.src)
			if seam != tc.want || graph != tc.want {
				t.Fatalf("seeded case: seam=%d callgraph=%d findings, want %d from each gate", seam, graph, tc.want)
			}
		})
	}
}
