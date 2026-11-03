// Purpose: the one classifier of the macOS security tool's "not found"
// failure, shared by the darwin custody backend and the harness credential
// keychain store (which compiles on every platform so its tests run there).
// Inputs: an error from a commandRunner. Outputs: true only when the
// captured stderr positively says the item is missing.

package secrets

import (
	"errors"
	"strings"
)

// isKeychainNotFound classifies the security tool's "not found" failure.
// It reads the tool's own stderr text; anything it cannot positively
// classify is NOT treated as not-found, so an unreadable keychain surfaces
// as unavailable rather than as a missing secret.
func isKeychainNotFound(err error) bool {
	var re *runnerError
	if !errors.As(err, &re) {
		return false
	}
	text := strings.ToLower(re.stderr)
	return strings.Contains(text, "could not be found") ||
		strings.Contains(text, "specified item could not be found") ||
		strings.Contains(text, "errsecitemnotfound")
}
