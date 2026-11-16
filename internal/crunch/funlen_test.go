package crunch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTestCheckSymbolsLength ensures TestCheckSymbols is not too long (max 50 lines).
func TestTestCheckSymbolsLength(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "contract_check_test.go", nil, 0)
	if err != nil {
		t.Fatalf("failed to parse file: %v", err)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "TestCheckSymbols" {
			start := fset.Position(fn.Body.Lbrace).Line
			end := fset.Position(fn.Body.Rbrace).Line
			length := end - start + 1
			if length > 50 {
				t.Fatalf("Function 'TestCheckSymbols' is too long (%d > 50)", length)
			}
		}
		return true
	})
}
