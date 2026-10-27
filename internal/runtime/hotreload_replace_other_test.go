//go:build !windows

package runtime_test

// Purpose: replaceFile never retries off Windows; a failed rename is a failure.
// SPORT: runtime/hot-reload-engine tests (P1-SEC-24).

// transientReplaceError is always false off Windows.
func transientReplaceError(error) bool { return false }
