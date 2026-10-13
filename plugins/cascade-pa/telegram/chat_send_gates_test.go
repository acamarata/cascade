// Purpose (this file): fix18 round 3's proofs for three Send guarantees the
//   round-2 suite left untested (the confirming review's mutations
//   "canonical", "bindfailopen" and "inflight" all survived it):
//   - only the CANONICAL thread id is a bridge thread (K3);
//   - a binding store that cannot answer refuses (fail closed, K3);
//   - Drain waits for an in-flight Send, not only inbound handling (K6).
//
// SPORT: plugins/cascade-pa/telegram TestChatBridge_Send gates/TEST (fix18).

package telegram

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestIsBridgeThread_CanonicalOnly: with chat 1 bound to its canonical
// legacy thread, only that exact id is a bridge thread; every alias of it
// (and every unmapped legacy-form id) is not, and Bind refuses each alias
// with nothing written. Dropping chatIDForThread's canonical round-trip
// lets ThreadMap.Bind accept "+1" or "01" for chat 1 and this goes RED.
func TestIsBridgeThread_CanonicalOnly(t *testing.T) {
	r := newBridgeRig(t)
	r.pairLegacy(t, 1)
	r.bind(t, "2")
	owner := Chat{ID: 2, Type: "private"}
	cases := map[string]bool{
		bridgeThreadPrefix + "1":      true,
		bridgeThreadPrefix + "-5":     false,
		bridgeThreadPrefix + "+1":     false,
		bridgeThreadPrefix + "01":     false,
		bridgeThreadPrefix + "-0":     false,
		bridgeThreadPrefix + " 1":     false,
		bridgeThreadPrefix + "1 ":     false,
		bridgeThreadPrefix:            false,
		"bridge-telegram-other:12345": false,
	}
	for threadID, want := range cases {
		if got, err := r.bridge.IsBridgeThread(context.Background(), threadID); err != nil || got != want {
			t.Errorf("IsBridgeThread(%q) = %v, %v; want %v", threadID, got, err, want)
		}
		if want || !strings.HasPrefix(threadID, bridgeThreadPrefix) {
			continue // a non-legacy id is an ordinary (minted-shape) id, bindable by design
		}
		if _, err := r.threads.Bind(context.Background(), owner, 2, threadID); err == nil {
			t.Errorf("ThreadMap.Bind(chat 2, %q) accepted a non-canonical or foreign legacy id", threadID)
		}
	}
	if bound, ok, err := r.threads.ThreadFor(context.Background(), 2); err != nil || ok {
		t.Fatalf("chat 2 bound to %q (%v, %v) after only refused binds", bound, ok, err)
	}
}

// TestChatBridge_Send_AliasThread_SendsNothing is the attack the canonical
// check exists for: the operator's bound chat 9016, and an ALIAS thread
// "+9016" whose own privacy row says public. Without the canonical check the
// alias resolves to the bound chat and its public row lets the body out.
func TestChatBridge_Send_AliasThread_SendsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	r.pairLegacy(t, 9016) // the canonical thread IS mapped; its alias must not resolve to it
	alias := bridgeThreadPrefix + "+9016"
	r.privacy.set(alias, cascadepa.TierPublic)

	err := r.bridge.Send(context.Background(), alias, []byte("aliased body"))
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Send(%q) = %v, want KindNotFound (not a bridge thread)", alias, err)
	}
	if got := r.doer.methods(); len(got) != 0 {
		t.Fatalf("an alias thread reached the transport: %v", got)
	}
}

// TestChatBridge_Send_BindingStoreError_FailsClosed proves a binding store
// that cannot answer refuses the Send: typed errSendNotBound, one
// bridge.refused event, zero transport calls. Inverting the gate to
// "berr == nil && !bound" lets the Send through and turns this RED.
func TestChatBridge_Send_BindingStoreError_FailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	threadID := r.pairLegacy(t, 9017) // the map has its own state; only the binding store fails
	r.privacy.set(threadID, cascadepa.TierPublic)
	r.state.mu.Lock()
	r.state.loadErr = errors.New("test: binding store unreachable")
	r.state.mu.Unlock()

	err := r.bridge.Send(context.Background(), threadID, []byte("while the store is down"))
	if err != errSendNotBound {
		t.Fatalf("Send = %v, want errSendNotBound", err)
	}
	if got := r.doer.methods(); len(got) != 0 {
		t.Fatalf("a Send with an unreadable binding reached the transport: %v", got)
	}
	if events := r.divergence.snapshot(); len(events) != 1 || events[0].Reason != reasonSendNotBound {
		t.Fatalf("divergence events = %+v, want exactly one with reason %q", events, reasonSendNotBound)
	}
}

// blockingPrivacy holds every ThreadPrivacy call until release is closed,
// so a Send can be observed while it is in flight.
type blockingPrivacy struct {
	arrived chan struct{}
	release chan struct{}
}

func (p *blockingPrivacy) ThreadPrivacy(context.Context, string) (cascadepa.SensitivityTier, error) {
	close(p.arrived)
	<-p.release
	return cascadepa.TierPublic, nil
}

// TestChatBridge_Drain_WaitsForInFlightSend proves Drain counts Send: while
// a Send is held inside its privacy lookup, Drain with a short budget must
// report the budget ran out; once released, Drain returns nil. Dropping
// Send's inFlight counting makes the first Drain return nil at once (RED).
func TestChatBridge_Drain_WaitsForInFlightSend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	privacy := &blockingPrivacy{arrived: make(chan struct{}), release: make(chan struct{})}
	seams := r.seams()
	seams.Privacy = privacy
	bridge := NewTelegramBridge(r.module, r.stores.Binding, testSubject, seams)
	threadID := r.pairLegacy(t, 9018)

	sent := make(chan error, 1)
	go func() { sent <- bridge.Send(context.Background(), threadID, []byte("in flight")) }()
	<-privacy.arrived

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := bridge.Drain(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain during an in-flight Send = %v, want context.DeadlineExceeded", err)
	}

	close(privacy.release)
	if err := <-sent; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := bridge.Drain(context.Background()); err != nil {
		t.Fatalf("Drain after the Send finished: %v", err)
	}
}
