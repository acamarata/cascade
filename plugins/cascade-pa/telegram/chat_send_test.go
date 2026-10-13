// Purpose (this file): proves TelegramBridge.Send's fix18 refusal parity —
//   a Send refused by the real egress Guard's own tier decision publishes
//   the SAME bridge.refused divergence event handleInbound's refuse()
//   already publishes for the inbound direction, content-free, while still
//   returning the typed error and reaching the transport zero times.
//
// SPORT: plugins/cascade-pa/telegram TestChatBridge_Send_TierRefused/TEST
//   (fix18).

package telegram

import (
	"context"
	"runtime"
	"strings"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// TestChatBridge_Send_TierRefused_EmitsBridgeRefused proves a restricted
// thread's Send: (1) returns a typed error, (2) reaches the fake transport
// zero times, (3) records exactly one bridge.refused divergence event and
// one RefusalReport entry, content-free, with the same reason text the
// inbound refusal path uses for the identical tier. Mutation: remove
// Send's b.refuseSend call and this goes RED (zero divergence events)
// while the "sends nothing" half stays green — isolating fix18's own gap.
func TestChatBridge_Send_TierRefused_EmitsBridgeRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send is refused at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	threadID := r.pairLegacy(t, 9013) // fix18 K3: isolate the tier gate from the binding gate
	r.privacy.set(threadID, cascadepa.TierRestricted)

	err := r.bridge.Send(context.Background(), threadID, []byte("assistant reply"))
	if err == nil {
		t.Fatal("Send on a restricted thread succeeded, want a refusal")
	}
	if len(r.doer.methods()) != 0 {
		t.Fatalf("a refused Send still reached the transport: %v", r.doer.methods())
	}
	for _, sent := range r.doer.sentTexts() {
		if strings.Contains(sent, "assistant reply") {
			t.Fatalf("refused body reached the wire: %q", sent)
		}
	}

	events := r.divergence.snapshot()
	if len(events) != 1 {
		t.Fatalf("divergence events = %+v, want exactly one bridge.refused", events)
	}
	got := events[0]
	if got.ThreadID != threadID || got.ResolvedTier != "restricted" || got.Reason != reasonThreadRestricted {
		t.Fatalf("divergence event = %+v, want thread %s / %s / %q",
			got, threadID, cascadepa.TierRestricted, reasonThreadRestricted)
	}
	if strings.Contains(got.Reason, "assistant reply") {
		t.Fatal("divergence event carries message content")
	}

	if report := r.bridge.RefusalReport(); len(report) != 1 || report[0].Reason != reasonThreadRestricted {
		t.Fatalf("RefusalReport = %+v, want one entry with reason %q", report, reasonThreadRestricted)
	}
}

// TestChatBridge_Send_NotBound_Refuses proves K3 (round 2): a thread whose
// chat id is not on the bridge's current paired binding refuses typed,
// content-free bridge.refused, zero transport calls — even for an
// otherwise-ALLOW tier. Mutation: remove Send's binding check and this
// goes RED (Send succeeds, reaching the transport).
func TestChatBridge_Send_NotBound_Refuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send is refused at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	threadID := threadIDForChat(9014)
	// Mapped, but deliberately NOT bound: no r.bind(t, "9014") call.
	if _, err := r.mapStore.Bind(context.Background(), testSubject, 9014, threadID); err != nil {
		t.Fatalf("seed map row: %v", err)
	}
	r.privacy.set(threadID, cascadepa.TierPublic)

	err := r.bridge.Send(context.Background(), threadID, []byte("assistant reply"))
	if err == nil {
		t.Fatal("Send on an unbound chat succeeded, want a refusal")
	}
	if len(r.doer.methods()) != 0 {
		t.Fatalf("a refused (unbound) Send still reached the transport: %v", r.doer.methods())
	}
	events := r.divergence.snapshot()
	if len(events) != 1 || events[0].ThreadID != threadID || events[0].Reason != reasonSendNotBound {
		t.Fatalf("divergence events = %+v, want exactly one bridge.refused with reason %q", events, reasonSendNotBound)
	}
}

// TestChatBridge_Send_PrivacyResolverError_FailsClosed proves K5 (round 2):
// an unresolvable thread tier (the resolver itself errors, §5.16's
// fail-closed rule) must follow TierLocalOnly — never fall open to an
// ALLOW tier. Mutation (CR-found, round 2): `tier = cascadepa.TierInternal`
// in place of TierLocalOnly on a resolver error made every existing test
// suite pass anyway; this test pins the exact fallback tier AND proves
// zero transport calls, so that specific mutation goes RED here.
func TestChatBridge_Send_PrivacyResolverError_FailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send is refused at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	threadID := r.pairLegacy(t, 9015) // isolate the privacy-resolver gate from the binding gate
	r.privacy.err = errTestGateClosed

	err := r.bridge.Send(context.Background(), threadID, []byte("assistant reply"))
	if err == nil {
		t.Fatal("Send with an unresolvable thread tier succeeded, want a fail-closed refusal")
	}
	if len(r.doer.methods()) != 0 {
		t.Fatalf("a refused (unresolvable tier) Send still reached the transport: %v", r.doer.methods())
	}
	events := r.divergence.snapshot()
	if len(events) != 1 || events[0].ResolvedTier != "local-only" {
		t.Fatalf("divergence events = %+v, want exactly one bridge.refused with ResolvedTier %q",
			events, cascadepa.TierLocalOnly)
	}
}

// TestChatBridge_Send_UnknownThread_NoDivergenceEvent proves the
// caller-error refusal (a threadID the map does not hold) is NOT a
// policy refusal: it publishes no bridge.refused event, unlike a tier or
// secret-gate refusal.
func TestChatBridge_Send_UnknownThread_NoDivergenceEvent(t *testing.T) {
	r := newBridgeRig(t)
	if err := r.bridge.Send(context.Background(), "not-a-bridge-thread", []byte("x")); err == nil {
		t.Fatal("Send on a foreign thread id succeeded, want a refusal")
	}
	if events := r.divergence.snapshot(); len(events) != 0 {
		t.Fatalf("divergence events = %+v, want zero for an unmapped thread id", events)
	}
}
