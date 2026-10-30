// Purpose: Windows stub for RunCredentialHelper. Credential helpers are
// not supported on Windows; callers must use key_env instead. This file
// exists so the package compiles on GOOS=windows and tests can assert
// the refusal without build-tag gymnastics.
// Inputs: HelperSpec from credhelper.go.
// Outputs: ErrHelperRefusedOnWindows (KindUnsupported).
// Constraints: no CGO, no exec. SPORT: internal/secrets credhelper windows (P1-GWY-02).

//go:build windows

package secrets

import (
	"context"
)

func runCredentialHelper(_ context.Context, _ HelperSpec) ([]byte, error) {
	return nil, ErrHelperRefusedOnWindows
}
