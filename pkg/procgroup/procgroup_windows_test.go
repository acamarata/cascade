//go:build windows

// Purpose: prove the windows half refuses honestly: no SysProcAttr, and a
//
//	typed KindUnsupported from Signal.
//
// SPORT: pkg/procgroup (TEST) — P1-PLG-09.

package procgroup

import (
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestProcgroupSignal(t *testing.T) {
	if Attr() != nil {
		t.Fatal("Attr() on windows = non-nil, want nil")
	}
	err := Signal(1234, syscall.SIGKILL)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("Signal on windows = %v, want KindUnsupported", err)
	}
}
