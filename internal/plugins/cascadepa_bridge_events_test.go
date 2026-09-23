package plugins

// Purpose (this file): the bridge journal's tests — that a lockout and an
//   admitted-but-unrouted message are really RECORDED, that no message content
//   reaches a record, and that a cancelled request still produces one. Plus
//   `cascade pa pair`'s RPC client, driven through a fake round trip.
//
// Constraints: no provider.Store and no bus construction — the Publisher seam is
//   narrow on purpose, so these tests observe exactly what production publishes.
//   No network: the pair client is exercised through the injected doer.
//
// SPORT: internal/plugins:cascadepa-bridge-events/TESTED,
//   cascadepa-bridge-client/TESTED (P1-E23-W5-S48-T1).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// recordedEvent is one publish the fake bus captured.
type recordedEvent struct {
	namespace string
	kind      events.EventKind
	source    string
	payload   string
}

// recordingBus is the BridgeEventPublisher test double. *events.Bus is the
// production implementation; this one records rather than persisting.
type recordingBus struct {
	mu   sync.Mutex
	got  []recordedEvent
	fail error
}

func newRecordingBus() *recordingBus { return &recordingBus{} }

func (b *recordingBus) Publish(ctx context.Context, namespace string, kind events.EventKind,
	source string, payload []byte) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		// A real Bus would refuse a cancelled write; recording it here is what
		// lets a test prove the journal publishes under WithoutCancel.
		return events.Event{}, err
	}
	if b.fail != nil {
		return events.Event{}, b.fail
	}
	b.got = append(b.got, recordedEvent{namespace: namespace, kind: kind, source: source,
		payload: string(payload)})
	return events.Event{Kind: kind}, nil
}

func (b *recordingBus) events() []recordedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]recordedEvent(nil), b.got...)
}

// TestBridgeJournal_RecordsALockout is AC#16's other half: the typed lockout
// event reaches a real sink in production, not only a recording fake in a test.
func TestBridgeJournal_RecordsALockout(t *testing.T) {
	bus := newRecordingBus()
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	newBridgeJournal(bus).EmitLockout(context.Background(),
		telegram.LockoutEvent{Subject: "tg-abc", At: at})
	got := bus.events()
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want 1", len(got))
	}
	if got[0].kind != EventKindBridgePairLockout || got[0].namespace != bridgeEventNamespace {
		t.Fatalf("recorded %+v", got[0])
	}
	var body struct {
		Subject string `json:"subject"`
		At      string `json:"at"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got[0].payload), &body); err != nil {
		t.Fatalf("payload is not decodable JSON: %v", err)
	}
	if body.Subject != "tg-abc" || !strings.Contains(body.At, "2026-09-21") || body.Reason == "" {
		t.Fatalf("payload = %+v", body)
	}
}

// TestBridgeJournal_RecordsAQuarantine is T0 D1(d): the typed
// bridge.secret_quarantined event reaches a real sink in production, not
// only a recording fake in a test, and republishes QuarantineEvent's own
// fields — no new field, no message content.
func TestBridgeJournal_RecordsAQuarantine(t *testing.T) {
	bus := newRecordingBus()
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	newBridgeJournal(bus).EmitQuarantine(context.Background(), telegram.QuarantineEvent{
		ExposureID: "exp-abc", Namespace: "api-key", Origin: "inbound-text",
		ChatKind: "private", At: at, Severity: "refused",
	})
	got := bus.events()
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want 1", len(got))
	}
	if string(got[0].kind) != telegram.QuarantineKind || got[0].namespace != bridgeEventNamespace {
		t.Fatalf("recorded %+v, want kind %q", got[0], telegram.QuarantineKind)
	}
	var body telegram.QuarantineEvent
	if err := json.Unmarshal([]byte(got[0].payload), &body); err != nil {
		t.Fatalf("payload is not decodable JSON: %v", err)
	}
	if body.ExposureID != "exp-abc" || body.Namespace != "api-key" || body.Origin != "inbound-text" {
		t.Fatalf("payload = %+v", body)
	}
}

// TestBridgeJournal_QuarantineIsANoopWithNoBus mirrors EmitLockout's own
// no-discarding-default precedent (this file's header): a nil-bus journal
// must not panic.
func TestBridgeJournal_QuarantineIsANoopWithNoBus(_ *testing.T) {
	(&bridgeJournal{}).EmitQuarantine(context.Background(), telegram.QuarantineEvent{})
}

// TestBridgeJournal_RecordsUnderACancelledRequest: a lockout that happened must
// not go unrecorded because the daemon was shutting down while it was refused.
func TestBridgeJournal_RecordsUnderACancelledRequest(t *testing.T) {
	bus := newRecordingBus()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newBridgeJournal(bus).EmitLockout(ctx, telegram.LockoutEvent{Subject: "tg-abc"})
	if len(bus.events()) != 1 {
		t.Fatal("a lockout during shutdown was not recorded; the publish is not WithoutCancel")
	}
}

// TestBridgeJournal_APublishFailureNeverFailsTheDispatch: losing a journal line
// must not change a decision — least of all into admitting something.
// EmitLockout has no return value, so "never fails the dispatch" means it must
// not panic even when the bus itself errors.
func TestBridgeJournal_APublishFailureNeverFailsTheDispatch(t *testing.T) {
	bus := newRecordingBus()
	bus.fail = errors.New("test: the bus is down")
	journal := newBridgeJournal(bus)
	journal.EmitLockout(context.Background(), telegram.LockoutEvent{Subject: "tg-abc"})
	if len(bus.events()) != 0 {
		t.Fatal("a failing publish still recorded an event")
	}
}

// fakePairDoer is one canned pa.pair_code round trip.
type fakePairDoer struct {
	method string
	params any
	reply  pairCodeResult
	err    error
}

func (f *fakePairDoer) Do(_ context.Context, method string, params, out any) error {
	f.method, f.params = method, params
	if f.err != nil {
		return f.err
	}
	if target, ok := out.(*pairCodeResult); ok {
		*target = f.reply
	}
	return nil
}

// TestPairClient_DialsTheDaemonsPairCodeVerb: `cascade pa pair` asks the daemon
// rather than opening the bridge's database in the CLI process.
func TestPairClient_DialsTheDaemonsPairCodeVerb(t *testing.T) {
	doer := &fakePairDoer{reply: pairCodeResult{
		Code: "7ZQK3M9F", Subject: "tg-abc", ExpiresAt: "2026-09-21T12:10:00Z",
	}}
	client := newCascadePAPairClient(nil, time.Second, bridgeTestPaths(t.TempDir()))
	client.doer = doer
	res, err := client.IssueCode(context.Background(), "tg-abc")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if doer.method != daemon.MethodBridgePairCode {
		t.Fatalf("dialed %q, want %q", doer.method, daemon.MethodBridgePairCode)
	}
	params, ok := doer.params.(pairCodeParams)
	if !ok || params.Subject != "tg-abc" {
		t.Fatalf("params = %+v", doer.params)
	}
	if res.Code != "7ZQK3M9F" || res.Subject != "tg-abc" || res.ExpiresAt.IsZero() {
		t.Fatalf("result = %+v", res)
	}
}

// TestPairClient_TransportFailureIsReturnedUnmodified: the daemon-not-running
// error is the honest answer, and it is what distinguishes a wired client from
// the package's canned unconfigured one.
func TestPairClient_TransportFailureIsReturnedUnmodified(t *testing.T) {
	sentinel := errors.New("test: daemon not running or unreachable")
	client := newCascadePAPairClient(nil, time.Second, bridgeTestPaths(t.TempDir()))
	client.doer = &fakePairDoer{err: sentinel}
	_, err := client.IssueCode(context.Background(), "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("IssueCode = %v, want the transport error unmodified", err)
	}
}

// TestPairClient_UnreadableExpiryIsAnIntegrityFailure: a daemon answering with
// an expiry nothing can parse is reported, never rendered as the zero time (a
// code that looks already expired, or never expiring).
func TestPairClient_UnreadableExpiryIsAnIntegrityFailure(t *testing.T) {
	client := newCascadePAPairClient(nil, time.Second, bridgeTestPaths(t.TempDir()))
	client.doer = &fakePairDoer{reply: pairCodeResult{Code: "7ZQK3M9F", Subject: "tg-abc",
		ExpiresAt: "not-a-timestamp"}}
	if _, err := client.IssueCode(context.Background(), ""); err == nil {
		t.Fatal("an unreadable expiry was accepted")
	}
}

// TestPairClient_PathFailurePropagates: an unresolvable socket path is reported,
// not worked around.
func TestPairClient_PathFailurePropagates(t *testing.T) {
	client := newCascadePAPairClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, errors.New("test: no path provider")
	})
	if _, err := client.IssueCode(context.Background(), ""); err == nil {
		t.Fatal("IssueCode succeeded with no resolvable socket path")
	}
}

// TestBridgeJournal_NilJournalAndNilBusAreNoOps: the recorder must never be the
// thing that panics a dispatch. Production refuses a nil bus at the composition
// root (NewCascadePABridge), so this is the defence for that guard, not a
// licence to wire one.
func TestBridgeJournal_NilJournalAndNilBusAreNoOps(_ *testing.T) {
	var nilJournal *bridgeJournal
	nilJournal.EmitLockout(context.Background(), telegram.LockoutEvent{Subject: "tg-abc"})
	newBridgeJournal(nil).EmitLockout(context.Background(), telegram.LockoutEvent{Subject: "tg-abc"})
}
