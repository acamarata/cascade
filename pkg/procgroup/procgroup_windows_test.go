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

func TestProcgroupLookupWindows(t *testing.T) {
	if _, err := Lookup(1234); err == nil {
		t.Fatal("Lookup on windows succeeded")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("Lookup on windows = %v, want KindUnsupported", err)
	}
	if Exists(1234) || Empty(1234) {
		t.Fatal("Exists or Empty on windows reported a verdict it cannot know")
	}
	if SigTerm != syscall.SIGTERM || SigKill != syscall.SIGKILL {
		t.Fatal("SigTerm/SigKill do not match the platform signals")
	}
}
