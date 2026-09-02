// Package violation (dotimport_violation.go) proves the dot-import evasion
// half: a dot-imported "os" makes Stdout callable unqualified, which is not
// even a *ast.SelectorExpr and so cannot be caught by selector matching at
// all — the gate rejects the dot-import declaration itself.
package violation

import . "os"

func WriteDotImported(msg string) {
	Stdout.Write([]byte(msg))
}
