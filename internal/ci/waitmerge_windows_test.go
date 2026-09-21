//go:build windows

// Purpose: the Windows (tier-2) half of the platform gate, asserted
// NATIVELY on the windows/amd64 CI lane (R-16.47) -- module_windows.go's
// own precedent applied to this ticket's build-tag pair.
//
// Constraints: asserted by IDENTITY AND MESSAGE TEXT, never errors.Is: a
// cascade.Error's Is compares Kind only (lesson_errors_is_compares_kind_only),
// so errors.Is against errWaitWindowsTier2 would pass for ANY
// KindUnsupported error in the tree. The contract's exact string is
// written out here as its own literal.
//
// SPORT: internal.ci.waitDaemonAbsentRefusal/TEST (P1-E25-W5-S51-T3).
package ci

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const wantWaitWindowsRefusal = "cascade github ci wait requires the daemon (Windows tier-2)"

func TestWaitDaemonAbsentRefusal_WindowsRefuses(t *testing.T) {
	err := waitDaemonAbsentRefusal()
	if err == nil {
		t.Fatal("waitDaemonAbsentRefusal returned nil on windows")
	}
	if !strings.Contains(err.Error(), wantWaitWindowsRefusal) {
		t.Fatalf("err = %q, want it to contain %q", err.Error(), wantWaitWindowsRefusal)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("KindOf(err) does not carry KindUnsupported: %v", err)
	}
}
