//go:build windows

// Purpose: the Windows (tier-2) half of the platform gate, asserted NATIVELY
//   on the windows/amd64 CI lane (R-16.47). module_windows.go is what that
//   lane compiles, so the refusal this file asserts is the refusal that lane
//   actually runs.
//
// Constraints: asserted by IDENTITY AND MESSAGE TEXT. errors.Is is not enough:
//   pkg/cascade's Is compares the KIND only, so errors.Is(err, errWindowsTier2)
//   holds for any KindUnsupported error in the tree and the assertion could not
//   fail. The contract's exact string is spelled out here as its own literal,
//   so blanking the message in module.go turns this lane red.
//
// SPORT: plugins/cascade-pa/telegram windows-tier-2-refusal/TEST
//   (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantWindowsRefusal is the contract's verbatim text, written out here rather
// than read from the production constant.
const wantWindowsRefusal = "bridge requires the daemon (Windows tier-2)"

func TestTelegramWindowsTierTwoRefusal(t *testing.T) {
	rig := newDefaultRig(t)
	err := rig.module.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded on Windows tier-2")
	}
	if !strings.Contains(err.Error(), wantWindowsRefusal) {
		t.Fatalf("Start returned %q, want it to name %q", err.Error(), wantWindowsRefusal)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Start returned %v, want KindUnsupported", err)
	}
	if len(rig.doer.methods()) != 0 {
		t.Fatalf("the refused Start still reached the transport: %v", rig.doer.methods())
	}
}

// TestPlatformBridgeRefusal_IsTheTypedError pins the build-tag pair's own
// contract on this lane.
func TestPlatformBridgeRefusal_IsTheTypedError(t *testing.T) {
	refusal := platformBridgeRefusal()
	if refusal == nil {
		t.Fatal("platformBridgeRefusal returned nil on windows")
	}
	if !strings.Contains(refusal.Error(), wantWindowsRefusal) {
		t.Fatalf("refusal = %q, want it to name %q", refusal.Error(), wantWindowsRefusal)
	}
}
