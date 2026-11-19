package helm

import (
	"errors"
	"path"
)

// ErrM2283 reports deterministic logic found outside tested Go source.
var ErrM2283 = errors.New("M2283")

// CheckDeterministic enforces the deterministic-logic boundary for the
// helm package. Only Go source files are allowed to carry deterministic
// logic; model turns, shell scripts, text files, and every other input
// fail closed with ErrM2283. The check is in-memory and judges the
// supplied path by file extension only.
func CheckDeterministic(p string) error {
	if path.Ext(p) == ".go" {
		return nil
	}
	return ErrM2283
}
