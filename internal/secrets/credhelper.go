// Purpose: runs a digest-pinned credential helper executable with a
// minimal environment, bounded output and a hard timeout. The helper
// is the owner's program: this runner never reads a vault, keychain or
// secret service, never caches output, and never logs secret bytes.
// Inputs: HelperSpec naming the absolute path, expected SHA-256, HOME,
//
//	USER and timeout.
//
// Outputs: RunCredentialHelper returning one trimmed line of stdout or
//
//	a typed error naming the path and exit status only.
//
// Constraints: no CGO, no inherited env, no argv args, stdin /dev/null,
// stderr discarded, stdout capped at 4096 bytes + 1 sentinel. Windows
// refuses with KindUnsupported. Errors use the frozen 14-kind set.
// SPORT: internal/secrets credhelper (P1-GWY-02).

package secrets

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrHelperChanged indicates a digest mismatch.
var ErrHelperChanged = cascade.New(cascade.KindPermissionDenied, "credential helper digest mismatch")

// ErrHelperUnsafe indicates unsafe ownership, permissions or path.
var ErrHelperUnsafe = cascade.New(cascade.KindPermissionDenied, "credential helper unsafe path or permissions")

// ErrHelperOutput indicates invalid or excessive stdout.
var ErrHelperOutput = cascade.New(cascade.KindIntegrity, "credential helper output invalid")

// ErrHelperFailed indicates failure to start, a nonzero exit or timeout.
var ErrHelperFailed = cascade.New(cascade.KindUnavailable, "credential helper failed")

// ErrHelperRefusedOnWindows directs Windows callers to environment credentials.
var ErrHelperRefusedOnWindows = cascade.New(cascade.KindUnsupported, "credential helpers are not supported on windows; use key_env")

// maxHelperOutput is the maximum number of bytes read from a helper's
// stdout. One extra byte is read to detect overflow; anything above
// this limit yields ErrHelperOutput without consuming further input.
const maxHelperOutput = 4096

// HelperSpec configures one invocation of a credential helper.
type HelperSpec struct {
	// Path is the absolute path to the helper executable. Symlinks are
	// resolved before hashing and execution.
	Path string
	// SHA256 is the lowercase hex digest of the resolved file.
	SHA256 string
	// Home sets HOME for the helper process; it is never inherited.
	Home string
	// User sets USER and LOGNAME for the helper process.
	User string
	// Timeout bounds execution. Zero means 10s.
	Timeout time.Duration
}

func (s HelperSpec) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 10 * time.Second
}

// RunCredentialHelper executes the pinned helper and returns its single
// trimmed output line. Platform implementations live in credhelper_unix.go
// and credhelper_windows.go.
func RunCredentialHelper(ctx context.Context, spec HelperSpec) ([]byte, error) {
	return runCredentialHelper(ctx, spec)
}
