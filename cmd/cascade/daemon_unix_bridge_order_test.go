//go:build !windows

// Purpose: a source-level posture proof for daemon_unix.go's own ordering
//
//	contract (P1-E23-W5-S48-T4 FIX-0) — plugins.SetBridgeApprovalQueue must
//	be called, and it must be called BEFORE buildRPCServer, inside
//	platformDaemonRun. internal/plugins.WireApprovalBridge (called later,
//	from plugin_rpc.go's wireCascadePABridge, once buildRPCServer has run)
//	reads the injected queue through a package-level variable with no
//	return value on a mis-order — a reversed pair of lines compiles clean
//	and every existing test still passes, silently reopening FLAG-0
//	(S-48.T4 producer confirming review). This test pins the ONE guard
//	that would catch it: the AST shape of platformDaemonRun's own body,
//	the same technique this package's chat_platform_regression_test.go and
//	what_test.go already use for a source-level contract no runtime value
//	can observe.
//
// Falsifiable: swap the two call statements (or delete the
// SetBridgeApprovalQueue line) in daemon_unix.go and this test goes red
// while `go build ./...` stays green — the exact FLAG-B input the
// confirming review used.
//
// SPORT: cmd/cascade:daemon-unix-bridge-order/TEST (ADD) — P1-E23-W5-S48-T4
//
//	producer confirming review, FLAG-B.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// findFuncDecl returns the *ast.FuncDecl named name in file, or nil.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// orderedTopLevelCalls walks body's statement list (top level only, the
// same shallow scan what_test.go's TestWhatDelegatesToNewRecallWhatCmd
// uses) and returns, IN SOURCE ORDER, a name for every call this test
// cares about: "plugins.SetBridgeApprovalQueue" for a matching selector
// call, "buildRPCServer" for a matching local-function call. Anything else
// is skipped — this is a narrow posture check, not a general call graph.
func orderedTopLevelCalls(body *ast.BlockStmt) []string {
	var calls []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkg, ok := fun.X.(*ast.Ident)
			if ok && pkg.Name == "plugins" && fun.Sel.Name == "SetBridgeApprovalQueue" {
				calls = append(calls, "plugins.SetBridgeApprovalQueue")
			}
		case *ast.Ident:
			if fun.Name == "buildRPCServer" {
				calls = append(calls, "buildRPCServer")
			}
		}
		return true
	})
	return calls
}

// TestDaemonUnixBridgeQueueInjectionPrecedesRPCServer proves
// platformDaemonRun calls plugins.SetBridgeApprovalQueue before it calls
// buildRPCServer. WireApprovalBridge (internal/plugins) arms whatever
// queue was injected by the time it runs; if buildRPCServer's own call
// chain ever reached a bridge before this line ran, the queue would still
// be nil and the producer leg would arm on nothing — silently, since
// neither call has a return value a caller here could check.
func TestDaemonUnixBridgeQueueInjectionPrecedesRPCServer(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "daemon_unix.go", nil, 0)
	if err != nil {
		t.Fatalf("parse daemon_unix.go: %v", err)
	}
	fn := findFuncDecl(file, "platformDaemonRun")
	if fn == nil {
		t.Fatal("daemon_unix.go declares no platformDaemonRun function")
	}
	calls := orderedTopLevelCalls(fn.Body)

	injectIdx, serverIdx := -1, -1
	for i, name := range calls {
		switch name {
		case "plugins.SetBridgeApprovalQueue":
			if injectIdx == -1 {
				injectIdx = i
			}
		case "buildRPCServer":
			if serverIdx == -1 {
				serverIdx = i
			}
		}
	}
	if injectIdx == -1 {
		t.Fatal("platformDaemonRun no longer calls plugins.SetBridgeApprovalQueue")
	}
	if serverIdx == -1 {
		t.Fatal("platformDaemonRun no longer calls buildRPCServer")
	}
	if injectIdx > serverIdx {
		t.Fatalf("plugins.SetBridgeApprovalQueue is called AFTER buildRPCServer (call order %v); "+
			"WireApprovalBridge would arm on a nil queue", calls)
	}
}
