// Purpose (this file): proves bridge.go's exported shapes are exactly
//   what the ticket's contract names — the ChatBridge method set,
//   InboundMessage's fields (including the never-clearable Untrusted
//   marker), and RefusalRecord's fields — over a minimal fake adapter, so
//   a future signature drift on any of the three fails here rather than
//   only at the Telegram implementation's own compile-time assertion.
//
// SPORT: plugins/cascade-pa bridge-contract/TEST (P1-E23-W5-S48-T2).

package cascadepa

import (
	"context"
	"testing"
	"time"
)

// fakeChatBridge is the minimal ChatBridge implementation this file uses
// to prove the interface's exact method set is satisfiable with no more
// and no fewer methods than task 3 declares.
type fakeChatBridge struct {
	paired bool
	sent   []string
	in     chan InboundMessage
	refs   []RefusalRecord
}

func (f *fakeChatBridge) Start(context.Context) error { return nil }
func (f *fakeChatBridge) Stop(context.Context) error  { return nil }
func (f *fakeChatBridge) Drain(context.Context) error { return nil }
func (f *fakeChatBridge) Paired() bool                { return f.paired }

func (f *fakeChatBridge) Send(_ context.Context, threadID string, body []byte) error {
	f.sent = append(f.sent, threadID+":"+string(body))
	return nil
}

func (f *fakeChatBridge) Receive(context.Context) (<-chan InboundMessage, error) {
	return f.in, nil
}

func (f *fakeChatBridge) RefusalReport() []RefusalRecord { return f.refs }

var _ ChatBridge = (*fakeChatBridge)(nil)

func TestChatBridge_MinimalImplementationSatisfiesTheInterface(t *testing.T) {
	f := &fakeChatBridge{in: make(chan InboundMessage, 1)}
	ctx := context.Background()

	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Send(ctx, "t1", []byte("hi")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got, want := f.sent[0], "t1:hi"; got != want {
		t.Fatalf("sent = %q, want %q", got, want)
	}
	if f.Paired() {
		t.Fatal("Paired() = true on a fake that never set it")
	}
	if err := f.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := f.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := f.RefusalReport(); got != nil {
		t.Fatalf("RefusalReport = %v, want nil (none recorded)", got)
	}
}

func TestInboundMessage_UntrustedIsNeverClearedByThisType(t *testing.T) {
	now := time.Now().UTC()
	msg := InboundMessage{
		ThreadID: "t1", SenderRef: "555", Body: []byte("hello"),
		Received: now, Origin: OriginBridgeTelegram, Untrusted: true,
	}
	if !msg.Untrusted {
		t.Fatal("a directly-constructed InboundMessage with Untrusted:true read back false")
	}
	if msg.Origin != OriginBridgeTelegram {
		t.Fatalf("Origin = %q, want %q", msg.Origin, OriginBridgeTelegram)
	}
	// The struct itself does not enforce Untrusted=true (that is
	// telegram.stampInbound/toInboundMessage's job, proven at that
	// layer's own tests) — this test only proves the field exists with
	// the exact type and name the contract names, and that the two
	// Origin constants are the values every consumer switches on.
	if OriginBridgeTelegram == OriginBridgeWhatsApp {
		t.Fatal("OriginBridgeTelegram and OriginBridgeWhatsApp must be distinct")
	}
}

func TestRefusalRecord_FieldsRoundTrip(t *testing.T) {
	at := time.Now().UTC()
	r := RefusalRecord{
		ThreadID: "bridge-telegram:1", ResolvedTier: "local-only",
		Reason: "thread is local-only, cannot bridge", CorrelationID: "chat:1", At: at,
	}
	if r.ThreadID == "" || r.ResolvedTier == "" || r.Reason == "" || r.CorrelationID == "" || r.At.IsZero() {
		t.Fatalf("RefusalRecord = %+v, every field must be set by a real caller", r)
	}
}
