package telegram

// Purpose (this file): the go/parser guard over package telegram's own source:
//   no send or answer may reach Doer.Do except from a BotClient method that
//   opens on the owner guard. A seeded-violation test proves the checker can
//   fail.
//
// SPORT: plugins/cascade-pa/telegram owner-guard-ast/TEST (P1-CPA-17).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// guardedSends maps each outbound Bot API method to the guard call its
// BotClient method must open with. MethodGetUpdates is the inbound poll and
// needs none; any other Doer.Do method argument fails the test.
var guardedSends = map[string]string{
	"MethodSendMessage":         "outboundChatAllowed",
	"MethodAnswerCallbackQuery": "consumeCallback",
	"MethodGetUpdates":          "",
}

// TestEveryOutboundPathUsesTheChatGuard parses package telegram and fails when
// a send or answer reaches Doer.Do outside a BotClient method whose first
// statement is an `if` on the owner guard that returns.
func TestEveryOutboundPathUsesTheChatGuard(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	guarded := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, e.Name(), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				n, problems := checkSendSites(fset, fn)
				guarded += n
				for _, p := range problems {
					t.Error(p)
				}
			}
		}
	}
	if guarded < 3 {
		t.Fatalf("found %d guarded send sites, want at least 3 (SendMessage, sendApprovalButtons, AnswerCallbackQuery)", guarded)
	}
}

// TestSendSiteCheckerCatchesSeededViolations proves the checker above can fail:
// each seeded method breaks one rule and must be reported.
func TestSendSiteCheckerCatchesSeededViolations(t *testing.T) {
	src := `package telegram
func (c *BotClient) Good(ctx context.Context) error {
	if err := c.outboundChatAllowed(ctx, 1); err != nil {
		return err
	}
	return c.doer.Do(ctx, MethodSendMessage, nil, nil)
}
func (c *BotClient) NoGuard(ctx context.Context) error {
	return c.doer.Do(ctx, MethodSendMessage, nil, nil)
}
func (c *BotClient) IgnoredGuard(ctx context.Context) error {
	_ = c.outboundChatAllowed(ctx, 1)
	return c.doer.Do(ctx, MethodSendMessage, nil, nil)
}
func (c *BotClient) UnknownMethod(ctx context.Context) error {
	return c.doer.Do(ctx, "editMessageText", nil, nil)
}
func (c *BotClient) IneffectiveGuard(ctx context.Context) error {
	if err := c.outboundChatAllowed(ctx, 1); false {
		return err
	}
	return c.doer.Do(ctx, MethodSendMessage, nil, nil)
}
func Free(ctx context.Context, d Doer) error {
	return d.Do(ctx, MethodAnswerCallbackQuery, nil, nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "seed.go", src, 0)
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	want := map[string]int{"Good": 0, "NoGuard": 1, "IgnoredGuard": 1, "UnknownMethod": 1, "IneffectiveGuard": 1, "Free": 1}
	for _, d := range f.Decls {
		fn := d.(*ast.FuncDecl)
		n, problems := checkSendSites(fset, fn)
		if len(problems) != want[fn.Name.Name] || (fn.Name.Name == "Good" && n != 1) {
			t.Fatalf("%s: guarded=%d problems=%v, want %d problems", fn.Name.Name, n, problems, want[fn.Name.Name])
		}
	}
}

// checkSendSites reports how many guarded send calls fn holds and one problem
// for each send call fn makes without opening on the guard.
func checkSendSites(fset *token.FileSet, fn *ast.FuncDecl) (guarded int, problems []string) {
	guard, found := "", 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Do" {
			return true
		}
		id, isID := call.Args[1].(*ast.Ident)
		want, known := guardedSends[""]
		if isID {
			want, known = guardedSends[id.Name]
		}
		switch {
		case !known:
			problems = append(problems, fset.Position(call.Pos()).String()+": Doer.Do with a method argument this test does not recognise")
		case want != "":
			guard, found = want, found+1
		}
		return true
	})
	if found == 0 {
		return 0, problems
	}
	if !isBotClientMethod(fn) || !opensWithGuard(fn.Body, guard) {
		return 0, append(problems, fset.Position(fn.Pos()).String()+": sends over Doer.Do without opening on "+guard)
	}
	return found, problems
}

func isBotClientMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, isID := star.X.(*ast.Ident)
	return isID && id.Name == "BotClient"
}

// opensWithGuard reports whether the body's first statement is an `if` that
// calls name in its init or condition and returns from its body, so a guard
// whose result is ignored does not count.
func opensWithGuard(body *ast.BlockStmt, name string) bool {
	if len(body.List) == 0 {
		return false
	}
	ifs, ok := body.List[0].(*ast.IfStmt)
	if !ok || len(ifs.Body.List) == 0 {
		return false
	}
	if _, returns := ifs.Body.List[len(ifs.Body.List)-1].(*ast.ReturnStmt); !returns {
		return false
	}
	if !refusalCondition(ifs.Cond) {
		return false
	}
	hit := false
	for _, part := range []ast.Node{ifs.Init, ifs.Cond} {
		if part == nil {
			continue
		}
		ast.Inspect(part, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					hit = true
				}
			}
			return !hit
		})
	}
	return hit
}

// refusalCondition reports whether cond can actually refuse: `err != nil` or
// `!ok`, not a literal such as `false`.
func refusalCondition(cond ast.Expr) bool {
	switch c := cond.(type) {
	case *ast.BinaryExpr:
		return c.Op == token.NEQ
	case *ast.UnaryExpr:
		return c.Op == token.NOT
	}
	return false
}
