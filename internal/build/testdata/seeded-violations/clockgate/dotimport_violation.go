// Package violation (dotimport_violation.go) proves the dot-import
// evasion half of R-14.132: a dot-imported "time" makes Now() callable
// unqualified, which is not even a *ast.SelectorExpr and so cannot be
// caught by selector matching at all — the gate rejects the dot-import
// declaration itself.
package violation

import . "time"

func DotImportedNow() Time {
	return Now()
}
