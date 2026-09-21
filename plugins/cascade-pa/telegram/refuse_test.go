package telegram

// Purpose (this file): refuse.go's tests — the fail-closed unwired
//   default, inbound refusal at decode (both transports, before pairing),
//   the value-free warning, the outbound refusal, pairing's say() routing
//   through the same gate, and the wire-encoding socket-layer canary.
//
// SPORT: plugins/cascade-pa/telegram refuse-tests/TEST (P1-E23-W5-S48-T3).

import (
	"context"
	"strings"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// witnessSecretText is the ONE fixed string every test in this file feeds
// a fakeSecretScanner as its "credential-shaped" witness. It is obviously
// synthetic and never sent anywhere real (fakeDoer never reaches a
// socket).
const witnessSecretText = "WITNESS-AKIA0000000000000000-NOT-REAL"

// fakeSecretScanner drives refuse.go's pass-clean and detected branches
// without a real internal/secrets.Detector (refuse.go's header: this
// package may not import internal/). detectFor keys on the exact text a
// test passes. The zero value (nil map) is the PERMISSIVE default
// rig_test.go installs — distinct from a truly UNCONFIGURED (nil)
// *scanner*, which refuses everything (T0 D1).
type fakeSecretScanner struct {
	detectFor map[string]string // text -> class
}

func (f fakeSecretScanner) Scan(content []byte) SecretScanOutcome {
	if class, ok := f.detectFor[string(content)]; ok {
		return SecretScanOutcome{Detected: true, Class: class}
	}
	return SecretScanOutcome{}
}

// secretScannerFor builds a fakeSecretScanner that flags exactly text.
func secretScannerFor(text, class string) fakeSecretScanner {
	return fakeSecretScanner{detectFor: map[string]string{text: class}}
}

// recordingQuarantineSink captures every emitted event.
type recordingQuarantineSink struct{ events []QuarantineEvent }

func (s *recordingQuarantineSink) EmitQuarantine(_ context.Context, e QuarantineEvent) {
	s.events = append(s.events, e)
}

// TestBridgeSecretRefusedAtDecode: a secret-shaped inbound message is
// refused before any handler runs, on both transports, one quarantine
// event per refusal.
func TestBridgeSecretRefusedAtDecode(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		rig := newDefaultRig(t)
		rig.bind(t, "111")
		rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
		sink := &recordingQuarantineSink{}
		rig.module.quarantine = sink
		called := false
		rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
			called = true
			return nil
		})
		rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
		if called {
			t.Fatal("a handler ran for a secret-shaped message")
		}
		if len(sink.events) != 1 {
			t.Fatalf("quarantine events = %d, want 1", len(sink.events))
		}
		if sink.events[0].Origin != quarantineOriginInboundText {
			t.Fatalf("origin = %q, want %q", sink.events[0].Origin, quarantineOriginInboundText)
		}
	})
	t.Run("callback", func(t *testing.T) {
		rig := newDefaultRig(t)
		rig.bind(t, "111")
		rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
		sink := &recordingQuarantineSink{}
		rig.module.quarantine = sink
		called := false
		rig.module.RegisterHandler(HandlerCallbackQuery, func(context.Context, InboundMessage) error {
			called = true
			return nil
		})
		rig.module.dispatch(context.Background(), callbackUpdate(1, 111, witnessSecretText))
		if called {
			t.Fatal("a handler ran for secret-shaped callback data")
		}
		if len(sink.events) != 1 || sink.events[0].Origin != quarantineOriginInboundCallback {
			t.Fatalf("quarantine events = %+v", sink.events)
		}
	})
}

// TestBridgeSecretRefusedBeforePairing is D4: an unpaired sender's
// secret-shaped message, and a secret-shaped "/pair <token>", are both
// refused and quarantined before any pairing/binding lookup.
func TestBridgeSecretRefusedBeforePairing(t *testing.T) {
	t.Run("unpaired sender, ordinary text shape", func(t *testing.T) {
		rig := newDefaultRig(t)
		rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
		sink := &recordingQuarantineSink{}
		rig.module.quarantine = sink
		rig.module.dispatch(context.Background(), textUpdate(1, 999, 999, witnessSecretText))
		if got := lastSent(t, rig.doer); got != replyBridgeSecretRefused {
			t.Fatalf("reply = %q, want the fail-closed refusal, not a plain \"not paired\"", got)
		}
		if len(sink.events) != 1 {
			t.Fatalf("quarantine events = %d, want 1 for an unpaired sender's secret message", len(sink.events))
		}
	})
	t.Run("secret-shaped pair token", func(t *testing.T) {
		rig := newDefaultRig(t)
		witnessToken := "/pair " + witnessSecretText
		rig.module.secretScanner = secretScannerFor(witnessToken, "api-key")
		sink := &recordingQuarantineSink{}
		rig.module.quarantine = sink
		rig.module.dispatch(context.Background(), textUpdate(1, 999, 999, witnessToken))
		if got := lastSent(t, rig.doer); got != replyBridgeSecretRefused {
			t.Fatalf("reply = %q, want the refusal; a secret /pair attempt must never reach VerifyAndConsume", got)
		}
		if len(sink.events) != 1 {
			t.Fatalf("quarantine events = %d, want 1", len(sink.events))
		}
	})
}

// TestBridgeSecretWarningIsValueFree pins the reply to the fixed constant.
func TestBridgeSecretWarningIsValueFree(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "aws-secret-key-witness-class")
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	got := lastSent(t, rig.doer)
	if got != replyBridgeSecretRefused {
		t.Fatalf("reply = %q, want the fixed refusal text", got)
	}
	if !strings.Contains(got, "cascade vault set") {
		t.Fatal("the refusal reply does not name the local remediation path")
	}
}

// TestBridgeSecretWarningNeverVariesWithTheDetection proves value-freedom
// BEHAVIORALLY: two different witness texts/classes produce the
// byte-identical reply (CR finding 8: re-checking a fixed constant's own
// contents, once `got` is already pinned to it, cannot fail).
func TestBridgeSecretWarningNeverVariesWithTheDetection(t *testing.T) {
	cases := []struct{ text, class string }{
		{witnessSecretText, "api-key"},
		{"WITNESS-DIFFERENT-SECRET-SHAPE-00000", "high-entropy"},
	}
	replies := make([]string, len(cases))
	for i, c := range cases {
		rig := newDefaultRig(t)
		rig.bind(t, "111")
		rig.module.secretScanner = secretScannerFor(c.text, c.class)
		rig.module.dispatch(context.Background(), textUpdate(int64(i+1), 111, 111, c.text))
		replies[i] = lastSent(t, rig.doer)
	}
	if replies[0] != replies[1] {
		t.Fatalf("the refusal reply varied with the detection: %q vs %q", replies[0], replies[1])
	}
	for i, c := range cases {
		if strings.Contains(replies[0], c.text) {
			t.Fatalf("the refusal reply embedded witness text %d: %q", i, c.text)
		}
		if strings.Contains(replies[0], c.class) {
			t.Fatalf("the refusal reply embedded the matched class %d: %q", i, c.class)
		}
	}
}

// TestBridgeOutboundVaultMaterialRefused: text about to be SENT is refused
// before the transport is ever called; a quarantine event with
// Origin=outbound publishes, carrying the caller's real chat kind (D9).
func TestBridgeOutboundVaultMaterialRefused(t *testing.T) {
	rig := newDefaultRig(t)
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	rig.module.reply(context.Background(), 111, "private", witnessSecretText)
	got := lastSent(t, rig.doer)
	if got != replyBridgeSecretRefused {
		t.Fatalf("sent = %q, want the value-free refusal", got)
	}
	for _, sent := range rig.doer.sentTexts() {
		if strings.Contains(sent, witnessSecretText) {
			t.Fatalf("the witness secret text reached the transport: %q", sent)
		}
	}
	if len(sink.events) != 1 || sink.events[0].Origin != quarantineOriginOutbound {
		t.Fatalf("quarantine events = %+v", sink.events)
	}
	if sink.events[0].ChatKind != "private" {
		t.Fatalf("ChatKind = %q, want the caller's real chat kind %q (D9)", sink.events[0].ChatKind, "private")
	}
}

// TestBridgeOutboundAnswerVaultMaterialRefused: answer's twin, threading
// its own chat kind through the identical outbound gate.
func TestBridgeOutboundAnswerVaultMaterialRefused(t *testing.T) {
	rig := newDefaultRig(t)
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	rig.module.answer(context.Background(), "cb-1", "group", witnessSecretText)
	if got := lastSent(t, rig.doer); got != replyBridgeSecretRefused {
		t.Fatalf("sent = %q, want the value-free refusal", got)
	}
	if len(sink.events) != 1 || sink.events[0].ChatKind != "group" {
		t.Fatalf("quarantine events = %+v, want ChatKind=%q", sink.events, "group")
	}
}

// TestPairingSayRoutesThroughGuardOutbound is T0 D5: say applies the SAME
// outbound gate reply/answer do, not a transport call of its own.
func TestPairingSayRoutesThroughGuardOutbound(t *testing.T) {
	rig := newDefaultRig(t)
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	rig.module.pairer.say(context.Background(), rig.module, 111, witnessSecretText)
	got := lastSent(t, rig.doer)
	if got != replyBridgeSecretRefused {
		t.Fatalf("pairing's say sent %q, want the value-free refusal — say must route through guardOutbound", got)
	}
}

// TestBridgeOrdinaryTextAdmitted is T0 D1's fail-closed-default proof: with
// NO scanner configured (not the rig's permissive default), a message is
// refused, no handler runs, and NO quarantine event publishes — there is
// nothing detected to record, only a missing capability.
func TestBridgeOrdinaryTextAdmitted(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = nil // undo the rig's permissive default
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	called := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "hello there"))
	if called {
		t.Fatal("ordinary text was admitted with no scanner configured; the unwired default must refuse")
	}
	if got := lastSent(t, rig.doer); got != replyBridgeSecretRefused {
		t.Fatalf("reply = %q, want the fail-closed refusal", got)
	}
	if len(sink.events) != 0 {
		t.Fatalf("quarantine events = %d, want 0: an unconfigured scanner has nothing to "+
			"quarantine, only a missing capability", len(sink.events))
	}
}

// bodyCapturingPoster is AC#6's socket-layer canary fake: it captures the
// raw bytes httpDoer would POST, one layer below fakeDoer's params-struct
// recording (apicall.go's poster seam).
type bodyCapturingPoster struct {
	bodies [][]byte
}

func (p *bodyCapturingPoster) Post(_ context.Context, _ string, body []byte) ([]byte, error) {
	p.bodies = append(p.bodies, body)
	return []byte(`{"ok":true,"result":{"message_id":1}}`), nil
}

// TestBridgeOutboundSocketLayerNeverCarriesVaultedValue is AC#6's red-team
// assertion at the ACTUAL wire encoding (real httpDoer, body-capturing
// poster): guardOutbound must substitute the refusal before json.Marshal
// ever sees the witness secret.
func TestBridgeOutboundSocketLayerNeverCarriesVaultedValue(t *testing.T) {
	state := newMemState()
	clock := fixedTestClock{t0()}
	stores := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	poster := &bodyCapturingPoster{}
	doer := newAPIDoer(syntheticToken, poster)
	gate := &tierGate{}
	client := NewBotClient(testSubject, doer, gate, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, &recordingSink{}, clock)
	module := NewTelegramModule(testSubject, client, stores.Binding, pairer, nodeVerbPolicy(), clock)
	module.secretScanner = secretScannerFor(witnessSecretText, "api-key")

	module.reply(context.Background(), 111, "private", witnessSecretText)

	if len(poster.bodies) != 1 {
		t.Fatalf("poster saw %d posts, want 1", len(poster.bodies))
	}
	wire := string(poster.bodies[0])
	if strings.Contains(wire, witnessSecretText) {
		t.Fatalf("the witness secret text reached the wire-encoded POST body: %q", wire)
	}
	if !strings.Contains(wire, "cascade vault set") {
		t.Fatalf("wire body = %q, want it to carry the value-free refusal text", wire)
	}
}
