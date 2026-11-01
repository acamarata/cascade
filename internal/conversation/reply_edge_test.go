package conversation

// Purpose: the reply pipeline's edge paths: configuration refusals, a hub at
// its stream bound, finishing a stream twice or with an untyped error, and a
// subscriber whose context ends mid-stream.
// Constraints: in-memory hub and fixtures only.
// SPORT: internal.conversation.reply tests (ADD).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNewReplierRefusesBadConfig(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("x"), 4000)
	good := ReplyConfig{Store: f.store, Journal: f.js, Door: f.door, Counter: wordCounter{},
		Effects: f.effects, Hub: f.hub, Clock: f.clock, Budget: 4000}
	for name, mutate := range map[string]func(*ReplyConfig){
		"no store":    func(c *ReplyConfig) { c.Store = nil },
		"no hub":      func(c *ReplyConfig) { c.Hub = nil },
		"zero budget": func(c *ReplyConfig) { c.Budget = 0 },
	} {
		cfg := good
		mutate(&cfg)
		_, err := NewReplier(cfg)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		wantSentinel(t, err, errReplyConfig)
	}
	foreign := good
	foreign.Store = nonSQLStore{}
	_, err := NewReplier(foreign)
	if err == nil {
		t.Fatal("a store that is not the conversation store was accepted")
	}
	wantSentinel(t, err, errReplyStore)
}

// nonSQLStore satisfies Store without being the SQL conversation store.
type nonSQLStore struct{ Store }

func TestReplyHubStreamBoundAndFinishEdges(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	h, err := NewReplyHub(clock)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxReplyStreams; i++ {
		if _, err := h.open("th", "u", ownerCaller()); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if _, err := h.open("th", "u", ownerCaller()); !errors.Is(err, errStreamsFull) {
		t.Fatalf("open past the bound: %v, want errStreamsFull", err)
	}
	h2, _ := NewReplyHub(clock)
	h2.finish("no-such-stream", nil) // unknown: a no-op
	id, _ := h2.open("th", "u", ownerCaller())
	h2.finish(id, errors.New("an untyped failure"))
	h2.finish(id, nil) // already terminal: the first terminal frame stands
	h2.publish(id, "late")
	frames, err := h2.Subscribe(context.Background(), id, ownerCaller())
	if err != nil {
		t.Fatal(err)
	}
	var got []Frame
	for fr := range frames {
		got = append(got, fr)
	}
	if len(got) != 1 || got[0].Type != FrameError || got[0].Kind != cascade.KindInternal.String() {
		t.Fatalf("frames = %+v, want one internal error frame", got)
	}
}

func TestReplyHubSubscriberContextEnds(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	h, _ := NewReplyHub(clock)
	id, _ := h.open("th", "u", ownerCaller())
	h.publish(id, "first")
	ctx, cancel := context.WithCancel(context.Background())
	frames, err := h.Subscribe(ctx, id, ownerCaller())
	if err != nil {
		t.Fatal(err)
	}
	if fr := <-frames; fr.Delta != "first" {
		t.Fatalf("first frame = %+v", fr)
	}
	cancel() // waiting for the next frame: the pump must stop and close the channel
	select {
	case _, open := <-frames:
		if open {
			t.Fatal("an unexpected frame arrived after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pump kept running after the subscriber's context ended")
	}
}

// failingCounter fails every count.
type failingCounter struct{}

func (failingCounter) Count(context.Context, string) (int, error) {
	return 0, errors.New("counter down")
}

// flakyStore fails the history reads it is told to.
type flakyStore struct {
	Store
	turnsErr, segmentsErr error
}

func (s flakyStore) ListTurns(ctx context.Context, threadID string) ([]Turn, error) {
	if s.turnsErr != nil {
		return nil, s.turnsErr
	}
	return s.Store.ListTurns(ctx, threadID)
}

func (s flakyStore) ListSegments(ctx context.Context, turnID string) ([]Segment, error) {
	if s.segmentsErr != nil {
		return nil, s.segmentsErr
	}
	return s.Store.ListSegments(ctx, turnID)
}

func TestComposeRefusals(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("x"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "what is the plan")
	b := replyBinding{threadID: "th", turnID: user.ID}
	ctx := context.Background()

	if _, err := f.replier.compose(ctx, b, provider.SensitivityRestricted); err != nil {
		t.Fatalf("baseline compose: %v", err)
	}
	f.replier.cfg.Counter = failingCounter{}
	if _, err := f.replier.compose(ctx, b, provider.SensitivityRestricted); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("failing counter: %v, want unavailable", err)
	}
	f.replier.cfg.Counter = wordCounter{}
	f.replier.cfg.Budget = 1
	_, err := f.replier.compose(ctx, b, provider.SensitivityRestricted)
	if err == nil {
		t.Fatal("a target larger than the budget composed")
	}
	wantSentinel(t, err, errReplyOverBudget)
	f.replier.cfg.Budget = 4000

	boom := errors.New("store down")
	f.replier.cfg.Store = flakyStore{Store: f.store, turnsErr: boom}
	if _, err := f.replier.compose(ctx, b, provider.SensitivityRestricted); !errors.Is(err, boom) {
		t.Fatalf("turn listing failure: %v", err)
	}
	f.replier.cfg.Store = flakyStore{Store: f.store, segmentsErr: boom}
	if _, err := f.replier.compose(ctx, b, provider.SensitivityRestricted); !errors.Is(err, boom) {
		t.Fatalf("segment listing failure: %v", err)
	}
}

func TestResumePayloadReplyMapping(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("x"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "q")
	assistant := Turn{ID: NewTurnID("th", 1, RoleAssistant), ThreadID: "th", Seq: 1, Role: RoleAssistant, CreatedAt: 2, Origin: OriginLocal}
	seg := Segment{ID: NewSegmentID(assistant.ID, 0, SegmentText), TurnID: assistant.ID, Seq: 0, Kind: SegmentText, Content: "answer", CreatedAt: 2}
	p := journalPayload{Type: JournalEntryTurnAppend, Turn: &assistant, ReplyTo: user.ID, Segments: []Segment{seg}}
	ctx := context.Background()

	_, err := resumePayload(ctx, nonSQLStore{}, p)
	if err == nil {
		t.Fatal("a reply resumed against a store without the reply write")
	}
	wantSentinel(t, err, errReplyStore)
	n, err := resumePayload(ctx, f.store, p)
	if err != nil || n != 2 {
		t.Fatalf("first resume = %d, %v; want the turn and its segment applied", n, err)
	}
	if n, err := resumePayload(ctx, f.store, p); err != nil || n != 0 {
		t.Fatalf("second resume = %d, %v; want 0, the reply already committed", n, err)
	}
}
