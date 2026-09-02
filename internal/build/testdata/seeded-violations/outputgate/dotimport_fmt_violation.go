// Package violation (dotimport_fmt_violation.go) proves the dot-import
// rule also covers "fmt", not only "os" — a dot-imported fmt makes
// Println callable unqualified, the same evasion shape as the os
// dot-import next to it.
package violation

import . "fmt"

func PrintDotImported(msg string) {
	Println(msg)
}
