//go:build !windows

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestDaemonGatewaySourcesUseSnapshot(t *testing.T) {
	files := []string{
		"daemon_unix_conductor.go",
		"daemon_unix_provider_health.go",
		"wire_gateway_quota.go",
	}

	credentialVars := []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"HOME",
		"USER",
	}

	fset := token.NewFileSet()
	for _, filename := range files {
		node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", filename, err)
		}

		ast.Inspect(node, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok {
						if ident.Name == "os" {
							switch sel.Sel.Name {
							case "Getenv":
								if len(call.Args) > 0 {
									if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
										varName := strings.Trim(lit.Value, `"`)
										for _, cred := range credentialVars {
											if varName == cred {
												t.Errorf("%s: found os.Getenv(%q) call; must use credentialEnv snapshot instead", filename, varName)
											}
										}
									}
								}
							case "Environ":
								t.Errorf("%s: found os.Environ() call; must use credentialEnv snapshot instead", filename)
							}
						}
					}
				}
			}
			return true
		})
	}
}
