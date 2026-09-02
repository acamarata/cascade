// Package violation (fprint_target_violation.go) proves the R-14.137
// Fprint*-targeting-stdout decision: fmt.Fprintln itself is never denied
// (writing to a real io.Writer is the sanctioned pattern), but its first
// argument here IS the os.Stdout selector, which the general os.Stdout
// match catches regardless of which call it sits inside — no dedicated
// Fprint*-argument rule was needed.
package violation

import (
	"fmt"
	"os"
)

func FprintToStdout(msg string) {
	fmt.Fprintln(os.Stdout, msg)
}
