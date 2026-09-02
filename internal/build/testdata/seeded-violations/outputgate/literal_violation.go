// Package violation (literal_violation.go) proves the easy case: a
// literal, unaliased os.Stdout reference — the case forbidigo already
// catches too, proven here so the AST path is confirmed correct before the
// harder alias/dot-import cases below.
package violation

import "os"

func WriteLiteral(msg string) {
	os.Stdout.Write([]byte(msg))
}
