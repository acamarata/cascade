// Package violation (alias_violation.go) proves the R-14.132 evasion:
// forbidigo matches the selector TEXT "time.Now", so importing "time"
// under an alias defeats it completely. This gate resolves the alias via
// the file's own import declaration first.
package violation

import t "time"

func AliasedNow() t.Time {
	return t.Now()
}
