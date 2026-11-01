package conversation

// Purpose: the shared chat.reply test fixture: a real SQLite conversation
//   store, a real journal, a real audit effect log over a memory store, a
//   frozen clock, and a scripted door that records every model call with
//   the thread tier its context carried.
// SPORT: internal.conversation.reply/ADDED (tests) (P1-CPA-16).

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var replyTestInstant = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// wordCounter counts whitespace-separated words: deterministic, no model.
type wordCounter struct{}

func (wordCounter) Count(_ context.Context, text string) (int, error) {
	return len(strings.Fields(text)), nil
}

// doorCall is one model call the scripted door received.
type doorCall struct {
	req    provider.ModelRequest
	tier   provider.SensitivityTier // ThreadPrivacyFrom(ctx).Mode
	tiered bool                     // ThreadPrivacyFrom(ctx) reported ok
	stream bool
}

// scriptDoor is a recording ReplyDoor. Execute answers the context stages
// (classify with one label, which their shape check rejects; summarize with
// summary, or the prompt echoed back when summary is empty, which is never
// shorter). ExecuteStream emits events, optionally after gate, and with
// holdAfterFirst stops after the first event until the call is cancelled.
type scriptDoor struct {
	mu             sync.Mutex
	calls          []doorCall
	summary        string
	events         []provider.StreamEvent
	streamErr      error
	gate           chan struct{}
	started        chan struct{}
	holdAfterFirst bool
}

func newScriptDoor(reply ...string) *scriptDoor {
	d := &scriptDoor{started: make(chan struct{}, 64)}
	for _, part := range reply {
		d.events = append(d.events, provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: part})
	}
	d.events = append(d.events, provider.StreamEvent{Kind: provider.StreamEventDone})
	return d
}

func (d *scriptDoor) record(ctx context.Context, req provider.ModelRequest, stream bool) {
	tp, ok := conductor.ThreadPrivacyFrom(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, doorCall{req: req, tier: tp.Mode, tiered: ok, stream: stream})
}

func (d *scriptDoor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	d.record(ctx, req, false)
	if req.TaskClass == "summarize" {
		if d.summary != "" {
			return provider.ModelResponse{Output: d.summary}, nil
		}
		return provider.ModelResponse{Output: req.Inputs[0].Content + " echoed"}, nil
	}
	return provider.ModelResponse{Output: "label"}, nil
}

func (d *scriptDoor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	d.record(ctx, req, true)
	d.started <- struct{}{}
	if d.streamErr != nil {
		return nil, nil, d.streamErr
	}
	ch, stopped := make(chan provider.StreamEvent), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(ch)
		if d.gate != nil {
			select {
			case <-d.gate:
			case <-ctx.Done():
				return
			}
		}
		for i, ev := range d.events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			case <-stopped:
				return
			}
			if d.holdAfterFirst && i == 0 {
				select {
				case <-ctx.Done():
				case <-stopped:
				}
				return
			}
		}
	}()
	return ch, func() { once.Do(func() { close(stopped) }) }, nil
}

// snapshot returns every call so far.
func (d *scriptDoor) snapshot() []doorCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]doorCall(nil), d.calls...)
}

// streamCalls counts main reply dispatches.
func (d *scriptDoor) streamCalls() int {
	n := 0
	for _, c := range d.snapshot() {
		if c.stream {
			n++
		}
	}
	return n
}

// mainRequest returns the one main reply request.
func (d *scriptDoor) mainRequest(t *testing.T) provider.ModelRequest {
	t.Helper()
	for _, c := range d.snapshot() {
		if c.stream {
			return c.req
		}
	}
	t.Fatal("no main reply dispatch was made")
	return provider.ModelRequest{}
}

type replyFixture struct {
	store   Store
	js      JournalStore
	effects *audit.EffectLog
	hub     *ReplyHub
	door    *scriptDoor
	clock   *testkit.FrozenClock
	replier *Replier
	reg     *rpc.Registry
}

// newReplyFixture builds a Replier over fresh real stores with budget.
func newReplyFixture(t *testing.T, door *scriptDoor, budget int) *replyFixture {
	t.Helper()
	clock := testkit.NewFrozenClock(replyTestInstant)
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), clock, nil))
	if err != nil {
		t.Fatal(err)
	}
	return newReplyFixtureWith(t, newTestStore(t), newTestJournal(t), effects, door, clock, budget)
}

func newReplyFixtureWith(t *testing.T, store Store, js JournalStore, effects *audit.EffectLog, door *scriptDoor,
	clock *testkit.FrozenClock, budget int) *replyFixture {
	t.Helper()
	hub, err := NewReplyHub(clock)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReplier(ReplyConfig{Store: store, Journal: js, Door: door, Counter: wordCounter{},
		Effects: effects, Hub: hub, Clock: clock, Budget: budget})
	if err != nil {
		t.Fatalf("NewReplier: %v", err)
	}
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "caller principal not recognized")
	})
	r.RegisterHandlers(reg)
	return &replyFixture{store: store, js: js, effects: effects, hub: hub, door: door, clock: clock, replier: r, reg: reg}
}

func ownerCLICtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func ownerCaller() principal.Caller { return principal.Caller{OwnerCLI: true} }

func principalCtx(id, scope string) context.Context {
	p := &principal.Principal{ID: id, Kind: principal.KindSession, SessionID: "s-" + id, Scope: scope}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: p})
}

// seedReplyTurn appends one turn with one text segment per part.
func seedReplyTurn(t *testing.T, store Store, threadID string, role Role, origin string, prov provider.Provenance, parts ...string) Turn {
	t.Helper()
	ctx := context.Background()
	turns, err := store.ListTurns(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(len(turns))
	turn := Turn{ID: NewTurnID(threadID, seq, role), ThreadID: threadID, Seq: seq, Role: role, CreatedAt: 1,
		Origin: origin, Provenance: prov}
	if err := store.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	for i, part := range parts {
		seg := Segment{ID: NewSegmentID(turn.ID, int64(i), SegmentText), TurnID: turn.ID, Seq: int64(i),
			Kind: SegmentText, Content: part, CreatedAt: 1}
		if err := store.AppendSegment(ctx, seg); err != nil {
			t.Fatalf("seed segment: %v", err)
		}
	}
	return turn
}

// openAndReply opens a stream for turnID as ctx's caller and replies on it.
//
//nolint:revive // t-first is the idiomatic test-helper signature, not a context-ordering bug
func (f *replyFixture) openAndReply(t *testing.T, ctx context.Context, threadID, turnID string) (ReplyResult, error) {
	t.Helper()
	id, err := f.replier.open(ctx, threadID, turnID)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	return f.replier.Reply(ctx, ReplyRequest{StreamID: id})
}

// wantSentinel asserts err is want by identity and by message.
func wantSentinel(t *testing.T, err, want error) {
	t.Helper()
	if err != want || err.Error() != want.Error() { //nolint:errorlint // identity, not errors.Is (Kind only)
		t.Fatalf("err = %v, want the sentinel %q", err, want)
	}
}

// wantRPCError asserts eo carries want's code and exact message.
func wantRPCError(t *testing.T, eo *rpc.ErrorObject, want *cascade.Error) {
	t.Helper()
	if eo == nil || eo.Code != want.Kind.JSONRPCCode() || eo.Message != want.Error() {
		t.Fatalf("rpc error = %+v, want code %d message %q", eo, want.Kind.JSONRPCCode(), want.Error())
	}
}

// storedTurns returns threadID's turns.
func storedTurns(t *testing.T, store Store, threadID string) []Turn {
	t.Helper()
	turns, err := store.ListTurns(context.Background(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	return turns
}
