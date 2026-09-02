// Package violation (alias_violation.go) proves the R-14.137 evasion CR
// found in D/S-06.T5: forbidigo matches the selector TEXT "os.Stdout", so
// importing "os" under an alias defeats it completely. This gate resolves
// the alias via the file's own import declaration first.
package violation

import osalias "os"

func WriteAliased(msg string) {
	osalias.Stdout.Write([]byte(msg))
}
