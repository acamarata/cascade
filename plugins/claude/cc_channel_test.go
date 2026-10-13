package claude

// Purpose: proves cc_channel.go's capability-probe deferral, the startup
// authentication + revision pin against the captured fixtures, the
// Windows tier-2 platform contract, and ordered, atomic, bounded
// de-duplication (P1-E37-W8-S73-T1). Errors are checked by identity AND
// message, never by errors.Is alone (errors.Is compares Kind only here).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// wantExactly fails unless target itself (pointer identity) is on err's
// Unwrap chain and err's message contains msg.
func wantExactly(t *testing.T, err, target error, msg string) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target {
			if !strings.Contains(err.Error(), msg) {
				t.Fatalf("error %q lacks %q", err, msg)
			}
			return
		}
	}
	t.Fatalf("error %v does not carry sentinel %v", err, target)
}

// fixtureHandshake returns the captured initialize request params.
func fixtureHandshake(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cc-channel", "protocol-negotiation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Request struct {
			Params json.RawMessage `json:"params"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil || len(fx.Request.Params) == 0 {
		t.Fatalf("fixture request.params unreadable: %v", err)
	}
	return fx.Request.Params
}

// supportedProbe answers supported with payload framed under the token the
// channel presented (forge=true frames it under a different token).
func supportedProbe(payload []byte, forge bool, calls *int) probeFunc {
	return func(_ context.Context, token provider.SessionToken) (probeResult, error) {
		*calls++
		if forge {
			token[0] ^= 0xff
		}
		return probeResult{status: ProbeSupported, handshake: provider.ControlFrame{Token: token, Payload: payload}}, nil
	}
}

// frameLog records transport frames; fail makes every send fail.
type frameLog struct {
	mu     sync.Mutex
	frames []provider.ControlFrame
	fail   bool
}

func (l *frameLog) send(_ context.Context, _ string, f provider.ControlFrame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return errors.New("peer closed the stream")
	}
	l.frames = append(l.frames, f)
	return nil
}

func (l *frameLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.frames)
}

func TestCCChannelCapabilityProbeDeferral(t *testing.T) {
	if ProbeIndeterminate.Resolve() != ProbeUnsupported || ProbeStatus(99).Resolve() != ProbeUnsupported {
		t.Fatal("indeterminate/unknown probe status did not resolve to unsupported")
	}
	ctx := context.Background()
	log := &frameLog{}
	prod := newCCChannel("linux", indeterminateProbe, log.send)
	wantExactly(t, prod.Deliver(ctx, "s", Envelope{ID: "a"}), ErrChannelNotStarted, "before Start")
	wantExactly(t, prod.Start(ctx), ErrChannelUnsupported, "D-S73T1-1")
	wantExactly(t, prod.Deliver(ctx, "s", Envelope{ID: "a"}), ErrChannelUnsupported, "unsupported")
	unsupported := newCCChannel("linux", func(context.Context, provider.SessionToken) (probeResult, error) {
		return probeResult{status: ProbeUnsupported}, nil
	}, log.send)
	wantExactly(t, unsupported.Start(ctx), ErrChannelUnsupported, "unsupported")
	calls := 0
	forged := newCCChannel("linux", supportedProbe(fixtureHandshake(t), true, &calls), log.send)
	if err := forged.Start(ctx); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("a handshake under a foreign token started the channel: %v", err)
	}
	if forged.Deliver(ctx, "s", Envelope{ID: "a"}) == nil || log.count() != 0 {
		t.Fatalf("a deferred channel sent %d frames", log.count())
	}
	if refusingTransport(ctx, "s", provider.ControlFrame{}) != ErrChannelUnsupported {
		t.Fatal("the production transport does not refuse")
	}
}

func TestCCChannelProtocolPinNegotiates(t *testing.T) {
	ctx := context.Background()
	calls, log := 0, &frameLog{}
	ch := newCCChannel("darwin", supportedProbe(fixtureHandshake(t), false, &calls), log.send)
	if err := ch.Start(ctx); err != nil {
		t.Fatalf("the captured handshake did not negotiate revision 1: %v", err)
	}
	if err := ch.Deliver(ctx, "sess-1", Envelope{ID: "e1", DataClass: "restricted", Summary: "excerpt"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	assertCapturedShape(t, log.frames[0])
	upgraded := strings.Replace(string(fixtureHandshake(t)), "2025-11-25", "2099-01-01", 1)
	later := newCCChannel("darwin", supportedProbe([]byte(upgraded), false, &calls), log.send)
	if err := later.Start(ctx); err != provider.ErrHarnessIncompatible {
		t.Fatalf("an unknown harness revision started: %v, want exactly ErrHarnessIncompatible", err)
	}
	if err := later.Deliver(ctx, "sess-1", Envelope{ID: "e2"}); err != provider.ErrHarnessIncompatible {
		t.Fatalf("Deliver after an incompatible negotiation = %v, want the runtime refusal", err)
	}
	bad := newCCChannel("darwin", supportedProbe([]byte(`{"capabilities":{}}`), false, &calls), log.send)
	wantExactly(t, bad.Start(ctx), ErrHandshakeInvalid, "not a valid initialize request")
	huge := newCCChannel("darwin", supportedProbe(make([]byte, maxEnvelopeBytes+1), false, &calls), log.send)
	wantExactly(t, huge.Start(ctx), ErrHandshakeInvalid, "not a valid initialize request")
	offline := newCCChannel("darwin", func(context.Context, provider.SessionToken) (probeResult, error) {
		return probeResult{}, errors.New("harness not reachable")
	}, log.send)
	if err := offline.Start(ctx); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "harness not reachable") {
		t.Fatalf("a failed probe deferred with %v", err)
	}
}

// assertCapturedShape compares a sent frame with the captured notification
// in addressed-inbox-delivery.json: same method, same {content, meta} keys.
func assertCapturedShape(t *testing.T, frame provider.ControlFrame) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cc-channel", "addressed-inbox-delivery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		A struct {
			Notification channelNotification `json:"notification"`
		} `json:"A_sent_by_this_session"`
	}
	var sent channelNotification
	if json.Unmarshal(raw, &fx) != nil || json.Unmarshal(frame.Payload, &sent) != nil {
		t.Fatal("fixture or frame is not a channel notification")
	}
	if sent.Method != fx.A.Notification.Method || sent.JSONRPC != fx.A.Notification.JSONRPC || !frame.Token.Valid() {
		t.Fatalf("sent %s/%s (token valid=%v), captured %s", sent.JSONRPC, sent.Method, frame.Token.Valid(), fx.A.Notification.Method)
	}
	if !strings.HasPrefix(sent.Params.Content, inboxBlockHead+"\n"+inboxBlockOpen) || sent.Params.Meta["provenance"] != ProvenanceUntrusted {
		t.Fatalf("content is not an untrusted-source data block: %+v", sent.Params)
	}
}

func TestCCChannelPlatformContract(t *testing.T) {
	ctx := context.Background()
	for _, goos := range []string{"darwin", "linux", "windows"} {
		calls, log := 0, &frameLog{}
		ch := newCCChannel(goos, supportedProbe(fixtureHandshake(t), false, &calls), log.send)
		startErr, deliverErr := ch.Start(ctx), ch.Deliver(ctx, "s", Envelope{ID: "p"})
		if goos == "windows" {
			wantExactly(t, startErr, ErrWindowsUnsupported, "tier-2")
			wantExactly(t, deliverErr, ErrWindowsUnsupported, "not supported on Windows")
			if calls != 0 || log.count() != 0 {
				t.Fatalf("windows probed %d times and sent %d frames", calls, log.count())
			}
			continue
		}
		if startErr != nil || deliverErr != nil || log.count() != 1 {
			t.Fatalf("%s: start=%v deliver=%v frames=%d", goos, startErr, deliverErr, log.count())
		}
	}
}

func TestCCChannelDeduplicatesAtomicallyAndBounded(t *testing.T) {
	ctx := context.Background()
	calls, log := 0, &frameLog{}
	ch := newCCChannel("linux", supportedProbe(fixtureHandshake(t), false, &calls), log.send)
	if err := ch.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = ch.Deliver(ctx, "s", Envelope{ID: "same"}) }()
	}
	wg.Wait()
	if log.count() != 1 {
		t.Fatalf("8 concurrent deliveries of one id sent %d frames, want 1", log.count())
	}
	log.fail = true
	wantExactly(t, ch.Deliver(ctx, "s", Envelope{ID: "fails"}), ErrChannelRejected, "peer closed the stream")
	log.fail = false
	if err := ch.Deliver(ctx, "s", Envelope{ID: "fails"}); err != nil || log.count() != 2 {
		t.Fatalf("a rejected id was remembered: err=%v frames=%d", err, log.count())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ch.Deliver(canceled, "s", Envelope{ID: "c"}); !cascade.HasKind(err, cascade.KindCanceled) || log.count() != 2 {
		t.Fatalf("a canceled delivery = %v with %d frames", err, log.count())
	}
	for i := range maxRememberedIDs {
		_ = ch.Deliver(ctx, "s", Envelope{ID: "fill-" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)})
	}
	if len(ch.seen) != maxRememberedIDs || len(ch.order) != maxRememberedIDs {
		t.Fatalf("dedup set grew to %d/%d, bound %d", len(ch.seen), len(ch.order), maxRememberedIDs)
	}
	before := log.count()
	_ = ch.Deliver(ctx, "s", Envelope{ID: "same"})
	if log.count() != before+1 {
		t.Fatal("an id evicted from the bounded set was not sent again")
	}
	wantExactly(t, ch.Deliver(ctx, "s", Envelope{ID: strings.Repeat("i", maxEnvelopeBytes)}), ErrEnvelopeTooLarge, "over the 16384 byte bound")
}

func TestCCChannelOutOfRangeIsHarnessIncompatible(t *testing.T) {
	ctx := context.Background()
	calls, log := 0, &frameLog{}
	upgraded := strings.Replace(string(fixtureHandshake(t)), "2025-11-25", "2099-01-01", 1)
	ch := newCCChannel("darwin", supportedProbe([]byte(upgraded), false, &calls), log.send)
	err := ch.Start(ctx)
	wantExactly(t, err, provider.ErrHarnessIncompatible, "no compatible protocol version in range")
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("incompatible harness kind = %v, want KindConflict", err)
	}
	wantExactly(t, ch.Deliver(ctx, "sess-1", Envelope{ID: "oor"}), provider.ErrHarnessIncompatible, "no compatible protocol version in range")
	if log.count() != 0 {
		t.Fatalf("an out-of-range harness was sent %d frames", log.count())
	}
}

func TestCCChannelDedupesByEnvelopeID(t *testing.T) {
	ctx := context.Background()
	calls, log := 0, &frameLog{}
	ch := newCCChannel("darwin", supportedProbe(fixtureHandshake(t), false, &calls), log.send)
	if err := ch.Start(ctx); err != nil {
		t.Fatalf("the captured negotiation did not start the channel: %v", err)
	}
	for range 3 {
		if err := ch.Deliver(ctx, "sess-1", Envelope{ID: "dup-1", DataClass: "restricted", Summary: "excerpt"}); err != nil {
			t.Fatal(err)
		}
	}
	if log.count() != 1 {
		t.Fatalf("three deliveries of one envelope id sent %d frames, want 1", log.count())
	}
	if err := ch.Deliver(ctx, "sess-1", Envelope{ID: "dup-2"}); err != nil || log.count() != 2 {
		t.Fatalf("a different id was not sent: err=%v frames=%d", err, log.count())
	}
	if err := ch.Deliver(ctx, "sess-2", Envelope{ID: "dup-1"}); err != nil || log.count() != 3 {
		t.Fatalf("one id to a second target was suppressed: err=%v frames=%d", err, log.count())
	}
}

// failingGetStore fails the read every hydration transaction starts with.
type failingGetStore struct{ provider.Store }

type failingGetTx struct{ provider.Tx }

func (s failingGetStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, failingGetTx{Tx: tx}) })
}

func (failingGetTx) Get(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("read failed")
}

func TestInboxHydrationReadFailureIsTyped(t *testing.T) {
	h, _ := NewInboxHydration(failingGetStore{Store: storetest.NewMemStore()}, func() time.Time { return fixedNow })
	err := h.Enqueue(context.Background(), "s1", InboxEvent{ID: "rf"})
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("a failed queue read surfaced as %v", err)
	}
}
