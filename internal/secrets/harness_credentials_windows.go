//go:build windows

// Purpose: the windows refusal. Harness credential entries are not handled
// on windows: every call is KindUnsupported. SPORT: HarnessCredentials/ADDED.

package secrets

import (
	"os"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NewHarnessCredentials returns a HarnessCredentials whose every call is
// KindUnsupported.
func NewHarnessCredentials(custody Custody, clock runtime.Clock) (HarnessCredentials, error) {
	if custody == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: harness credentials need a custody and a clock")
	}
	return &harnessCredentials{store: unsupportedStore{}, custody: custody, clock: clock}, nil
}

// harnessFileOwner has no uid to report on windows, so the file store
// refuses every file.
func harnessFileOwner(os.FileInfo) (int, bool) { return 0, false }
