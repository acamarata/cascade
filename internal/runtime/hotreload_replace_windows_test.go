package runtime_test

// Purpose: which rename failures replaceFile may retry on Windows.
// SPORT: runtime/hot-reload-engine tests (P1-SEC-24).

import (
	"errors"
	"syscall"
)

// transientReplaceError reports a Windows sharing refusal of a rename:
// ERROR_ACCESS_DENIED (5) or ERROR_SHARING_VIOLATION (32). Nothing else.
func transientReplaceError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.ERROR_ACCESS_DENIED || errno == syscall.Errno(32))
}
