//go:build windows

// Purpose (this file): the Windows (tier-2) half of THIS ticket's own
//   surface — TelegramBridge, the ChatBridge wrapper — asserted NATIVELY
//   on the windows/amd64 CI lane (R-16.47), matching module_windows_test.go's
//   identical proof for the underlying TelegramModule (P1-E23-W5-S48-T1).
//   TestBridgeWindowsTierTwoRefusal_Send (S-49.T4 Q5) closes the gap the
//   original ticket left open: Send used to skip platformBridgeRefusal
//   entirely and would dial the Bot API on this platform.
//
// Constraints: TelegramBridge.Start/Send are direct delegations to
//   module.Start/platformBridgeRefusal, so this file proves the delegation
//   carries the refusal through — never a second, independent Windows
//   check. Identity AND message text, not errors.Is: pkg/cascade's Is
//   compares Kind only, so errors.Is would hold for any KindUnsupported
//   error in the tree.
//
// SPORT: plugins/cascade-pa/telegram windows-tier-2-refusal/TEST
//   (P1-E23-W5-S48-T2, P1-E23-W5-S49-T4).

package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBridgeWindowsTierTwoRefusal(t *testing.T) {
	rig := newDefaultRig(t)
	bridge := NewTelegramBridge(rig.module, rig.stores.Binding, testSubject,
		&fakeChatService{}, newFakeThreadPrivacy(), &fakeDivergenceSink{})

	err := bridge.Start(context.Background())
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

// TestBridgeWindowsTierTwoRefusal_Send is S-49.T4 Q5's fix proof: Send is a
// network-reaching entry point exactly like Start, and must refuse at the
// SAME platform gate before touching the transport (S-48.T2's Send did
// not — only module.go's Start called platformBridgeRefusal, so Send dialed
// the Bot API on this platform).
func TestBridgeWindowsTierTwoRefusal_Send(t *testing.T) {
	rig := newDefaultRig(t)
	bridge := NewTelegramBridge(rig.module, rig.stores.Binding, testSubject,
		&fakeChatService{}, newFakeThreadPrivacy(), &fakeDivergenceSink{})

	err := bridge.Send(context.Background(), threadIDForChat(9012), []byte("x"))
	if err == nil {
		t.Fatal("Send succeeded on Windows tier-2")
	}
	if !strings.Contains(err.Error(), wantWindowsRefusal) {
		t.Fatalf("Send returned %q, want it to name %q", err.Error(), wantWindowsRefusal)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Send returned %v, want KindUnsupported", err)
	}
	if len(rig.doer.methods()) != 0 {
		t.Fatalf("the refused Send still reached the transport: %v", rig.doer.methods())
	}
}
