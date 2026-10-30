// Purpose (this file): P1-CPA-14 acceptance [1]. A stored vault value in a
//   bridged send is replaced by the EgressClassBridge substitution pass
//   before the wire: the assertion reads the recorded sendMessage body,
//   requires it non-empty, free of the value and carrying the substitution
//   tag (an engine that returned nothing fails, the confirm's M3). A
//   credential-shaped body is refused at the secret gate by identity, and
//   ordinary text crosses unchanged.
//
// SPORT: plugins/cascade-pa/telegram TestPABridge_BridgeSecretScrub/TEST
//   (P1-CPA-14).

package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// scrubName and scrubValue are the synthetic stored secret. The value has
// no detector shape, so only the exact-value substitution pass can catch it.
const (
	scrubName  = "accept_test_secret"
	scrubValue = "SYNTHETIC-VAULT-VALUE-8140"
)

func TestPABridge_BridgeSecretScrub(t *testing.T) {
	const owner = int64(9401)
	r := newAcceptRig(t, scrubName, scrubValue)
	threadID := r.pairOwner(t, owner, provider.SensitivityInternal)
	if err := r.bridge.Send(context.Background(), threadID, []byte("the value is "+scrubValue)); err != nil {
		t.Fatalf("Send with a stored vault value: %v", err)
	}
	sent := r.sendMessages()
	if len(sent) != 1 || sent[0].ChatID != owner {
		t.Fatalf("sendMessage calls = %+v, want exactly one to the owner", sent)
	}
	body := sent[0].Text
	if strings.TrimSpace(body) == "" {
		t.Fatal("the recorded wire body is empty; a pass on empty output proves nothing")
	}
	if strings.Contains(body, scrubValue) {
		t.Fatalf("the stored value crossed the wire verbatim: %q", body)
	}
	if !strings.HasPrefix(body, "the value is <") || !strings.HasSuffix(body, ">") || !strings.Contains(body, "</") {
		t.Fatalf("wire body = %q, want the original text with the value replaced by a substitution tag", body)
	}
	assertSecretShapeRefused(t, r, threadID)
	assertInboundSecretQuarantined(t, r, owner)
}

// assertSecretShapeRefused: a credential-shaped body is refused with the
// typed errSendBodyRefused (identity and message) and never reaches the wire.
func assertSecretShapeRefused(t *testing.T, r *acceptRig, threadID string) {
	t.Helper()
	before := len(r.sendMessages())
	err := r.bridge.Send(context.Background(), threadID, []byte("key: "+acceptWitness()))
	if err != errSendBodyRefused { //nolint:errorlint // identity is the contract: errors.Is compares Kind only
		t.Fatalf("Send error = %v, want errSendBodyRefused by identity", err)
	}
	if !strings.Contains(err.Error(), "refused the body at the outbound secret gate; no content was delivered") {
		t.Fatalf("Send error message = %q", err.Error())
	}
	if n := len(r.sendMessages()); n != before {
		t.Fatalf("a refused body produced %d sendMessage calls", n-before)
	}
	ev := r.divergence.snapshot()
	if len(ev) != 1 || ev[0].Reason != reasonSendSecretRefused || ev[0].ThreadID != threadID {
		t.Fatalf("RefusalEvents = %+v, want one %q", ev, reasonSendSecretRefused)
	}
}

// assertInboundSecretQuarantined: a credential-shaped inbound DM is never
// appended, its value never reaches the wire, and exactly one value-free
// quarantine event is recorded.
func assertInboundSecretQuarantined(t *testing.T, r *acceptRig, owner int64) {
	t.Helper()
	r.module.dispatch(context.Background(), textUpdate(7, owner, owner, "here is a key: "+acceptWitness()))
	if n := r.chat.callCount(); n != 0 {
		t.Fatalf("AppendTurn calls = %d, want 0", n)
	}
	for _, p := range r.sendMessages() {
		if strings.Contains(p.Text, acceptWitness()) {
			t.Fatalf("the witness crossed the wire: %q", p.Text)
		}
	}
	var inbound []QuarantineEvent
	for _, e := range r.quarantine.events {
		if e.Origin == quarantineOriginInboundText {
			inbound = append(inbound, e)
		}
	}
	if len(inbound) != 1 {
		t.Fatalf("inbound quarantine events = %+v, want exactly 1", r.quarantine.events)
	}
	raw, err := json.Marshal(inbound[0])
	if err != nil {
		t.Fatalf("marshal quarantine event: %v", err)
	}
	if strings.Contains(string(raw), acceptWitness()) {
		t.Fatalf("the quarantine event carries the witness: %s", raw)
	}
}

// TestPABridge_BridgeSecretScrub_OrdinaryTextUnaffected is the contrast: the
// same real gates leave ordinary text byte-identical on the wire and in the
// appended turn, with no refusal and no quarantine event.
func TestPABridge_BridgeSecretScrub_OrdinaryTextUnaffected(t *testing.T) {
	const owner, text = int64(9402), "just an ordinary status update"
	r := newAcceptRig(t, scrubName, scrubValue)
	threadID := r.pairOwner(t, owner, provider.SensitivityInternal)
	if err := r.bridge.Send(context.Background(), threadID, []byte(text)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent := r.sendMessages(); len(sent) != 1 || sent[0].Text != text {
		t.Fatalf("sendMessage calls = %+v, want one carrying %q unchanged", sent, text)
	}
	r.module.dispatch(context.Background(), textUpdate(1, owner, owner, text))
	r.chat.mu.Lock()
	calls := append([]appendTurnCall(nil), r.chat.calls...)
	r.chat.mu.Unlock()
	if len(calls) != 1 || calls[0].text != text || calls[0].threadID != threadID {
		t.Fatalf("AppendTurn calls = %+v, want one with %q", calls, text)
	}
	if ev := r.divergence.snapshot(); len(ev) != 0 {
		t.Fatalf("ordinary text was refused: %+v", ev)
	}
	if len(r.quarantine.events) != 0 {
		t.Fatalf("ordinary text was quarantined: %+v", r.quarantine.events)
	}
}
