// Wire-contract coverage uses rpcDoer without opening real sockets.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// fakeRPCDoer records wire requests for append-only and memory fixtures.
type fakeRPCDoer struct {
	err          error
	fill         func(out any)
	calledMethod string
	gotParams    any
}

func (f *fakeRPCDoer) Do(_ context.Context, method string, params, out any) error {
	// Append-only fixtures stop here; replyRPCFixture covers generation.
	if method == "chat.reply_open" {
		return errors.New("fixture has no reply stream")
	}
	f.calledMethod = method
	f.gotParams = params
	if f.err != nil {
		return f.err
	}
	if f.fill != nil {
		f.fill(out)
	}
	return nil
}

func TestCascadePAOneShotRepliesThroughChatReply(t *testing.T) {
	d := &replyRPCFixture{t: t}
	got, err := (&cascadePAClient{doer: d}).OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
	if err != nil || got.Content != "durable reply" || got.ThreadID != "th" || got.TurnID != "a" {
		t.Fatalf("reply = %+v, error = %v", got, err)
	}
	if !reflect.DeepEqual(d.calls, []string{"chat.append_turn", "chat.reply_open", "chat.reply", "chat.get_thread"}) {
		t.Fatal(d.calls)
	}
}

func TestCascadePAStreamSubscribesBeforeReply(t *testing.T) {
	d := &replyRPCFixture{t: t}
	tokens, errs := (&cascadePAClient{doer: d}).Stream(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
	var got string
	for token := range tokens {
		got += token
	}
	if err := <-errs; err != nil || got != "live reply" {
		t.Fatalf("tokens %q, error %v", got, err)
	}
	if !reflect.DeepEqual(d.calls, []string{"chat.append_turn", "chat.reply_open", "subscribe", "chat.reply", "chat.get_thread"}) {
		t.Fatal(d.calls)
	}
}

type replyRPCFixture struct {
	t        *testing.T
	calls    []string
	frames   chan client.Event
	terminal bool
}

func (d *replyRPCFixture) Do(_ context.Context, method string, params, out any) error {
	d.calls = append(d.calls, method)
	var raw string
	switch method {
	case "chat.append_turn":
		if keys := wireKeys(d.t, params); keys["role"] != "user" {
			d.t.Error(keys)
		}
		raw = `{"thread_id":"th","turn_id":"u"}`
	case "chat.reply_open":
		if keys := wireKeys(d.t, params); keys["thread_id"] != "th" || keys["turn_id"] != "u" {
			d.t.Error(keys)
		}
		raw = `{"stream_id":"server-stream"}`
	case "chat.reply":
		if keys := wireKeys(d.t, params); len(keys) != 1 || keys["stream_id"] != "server-stream" {
			d.t.Error(keys)
		}
		if d.frames != nil {
			d.frames <- client.Event{Data: `{"type":"delta","thread_id":"th","delta":"live reply"}`}
			d.frames <- client.Event{Data: `{"type":"done","thread_id":"th"}`}
			d.terminal = true
			close(d.frames)
		}
		raw = `{"thread_id":"th","turn_id":"u","assistant_turn_id":"a"}`
	case "chat.get_thread":
		if d.frames != nil && !d.terminal {
			d.t.Error("read before terminal frame")
		}
		raw = `{"turns":[{"turn":{"ID":"a","Role":"assistant"},"segments":[{"Kind":"text","Content":"durable reply"}]}]}`
	default:
		d.t.Errorf("unexpected method %s", method)
	}
	return json.Unmarshal([]byte(raw), out)
}

func (d *replyRPCFixture) StreamTopic(_ context.Context, topic string, query url.Values) (<-chan client.Event, func(), error) {
	d.calls = append(d.calls, "subscribe")
	if topic != "chat.reply" || query.Get("stream_id") != "server-stream" {
		d.t.Error(topic, query)
	}
	d.frames = make(chan client.Event)
	return d.frames, func() {}, nil
}

// Leading context travels with the user turn.
func TestCascadePAClient_OneShot_SendsLeadingContext(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*appendTurnResult)
		res.ThreadID = "thread-1"
		res.TurnID = "turn-2"
	}}
	c := newCascadePAClient(nil, 0, nil)
	c.doer = doer

	_, _ = c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi", LeadingContext: "resumed block"})
	params, ok := doer.gotParams.(appendTurnParams)
	if !ok {
		t.Fatalf("Do params = %T, want appendTurnParams", doer.gotParams)
	}
	if params.LeadingContext != "resumed block" {
		t.Fatalf("appendTurnParams.LeadingContext = %q, want %q", params.LeadingContext, "resumed block")
	}
}

// Resume maps the daemon's composed context into the CLI result.
func TestCascadePAClient_Resume_SuccessMapsFields(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*resumeResult)
		*res = resumeResult{
			ThreadID: "th1", LeadingContext: "composed block", Tokens: 12, Trimmed: []string{"turn:x: dropped"},
		}
	}}
	c := newCascadePAClient(nil, 0, nil)
	c.doer = doer

	got, err := c.Resume(context.Background(), pacmd.ResumeRequest{TopicID: "th1", Thread: "session1"})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got.ThreadID != "th1" || got.LeadingContext != "composed block" || got.Tokens != 12 ||
		len(got.Trimmed) != 1 || got.Trimmed[0] != "turn:x: dropped" {
		t.Errorf("Resume: got %+v, did not map every chat.resume field", got)
	}
	params, ok := doer.gotParams.(resumeParams)
	if !ok || params.TopicID != "th1" || params.SessionThreadID != "session1" {
		t.Errorf("Resume params = %+v (ok=%v), want topic_id th1 into session_thread_id session1", doer.gotParams, ok)
	}
	if doer.calledMethod != "chat.resume" {
		t.Errorf("Resume dispatched method %q, want \"chat.resume\"", doer.calledMethod)
	}
}

// CreateThread maps the daemon's new thread ID.
func TestCascadePAClient_CreateThread_SuccessMapsThreadID(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*createThreadResult)
		res.ThreadID = "thread-new1"
	}}
	c := newCascadePAClient(nil, 0, nil)
	c.doer = doer

	got, err := c.CreateThread(context.Background(), "local-only")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if got != "thread-new1" {
		t.Errorf("CreateThread: got = %q, want %q", got, "thread-new1")
	}
	if doer.calledMethod != "chat.create_thread" {
		t.Errorf("CreateThread dispatched method %q, want \"chat.create_thread\"", doer.calledMethod)
	}
	params, ok := doer.gotParams.(createThreadParams)
	if !ok || params.PrivacyMode != "local-only" {
		t.Errorf("CreateThread params = %+v (ok=%v), want PrivacyMode=local-only", doer.gotParams, ok)
	}
}

func TestCascadePASoulClient_Show_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.SoulShowResult)
		*res = memory.SoulShowResult{Body: "the body", Schema: "v1", Version: 3, Diverged: true}
	}}
	c := newCascadePASoulClient(nil, 0, nil)
	c.doer = doer

	view, err := c.Show(context.Background())
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := cascadepa.SoulChatView{Body: "the body", Schema: "v1", Version: 3, Diverged: true}
	if view != want {
		t.Errorf("Show: view = %+v, want %+v", view, want)
	}
}

func TestCascadePASoulClient_Edit_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.SoulEditResult)
		res.Version = 4
	}}
	c := newCascadePASoulClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Edit(context.Background(), cascadepa.SoulChatDocument{Body: "new body", Schema: "v1"})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if res.Version != 4 {
		t.Errorf("Edit: res.Version = %d, want 4", res.Version)
	}
}

func TestCascadePAReviewClient_List_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ListResult)
		res.Pending = []memory.CandidateSummary{{ID: "c1", Kind: memory.KindUser, Sessions: 2, RefCount: 5}}
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	listing, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Pending) != 1 || listing.Pending[0].ID != "c1" || listing.Pending[0].RefCount != 5 {
		t.Errorf("List: listing = %+v, want one candidate c1 with RefCount 5", listing)
	}
}

func TestCascadePAReviewClient_Act_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ActResult)
		res.Action = "accept"
		res.Changed = true
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Act(context.Background(), "c1", "accept")
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if res.Action != "accept" || !res.Changed {
		t.Errorf("Act: res = %+v, want Action=accept Changed=true", res)
	}
}

func TestCascadePAReviewClient_Forget_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.ForgetResult)
		res.Forgotten = true
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Forget(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if !res.Forgotten {
		t.Error("Forget: res.Forgotten = false, want true")
	}
}

// TestCascadePAReviewClient_CheckDigest_Success proves CheckDigest's real
// arithmetic (len(listing.Pending)) over a real List success, not just
// its error-propagation branch (already covered separately).
func TestCascadePAReviewClient_CheckDigest_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ListResult)
		res.Pending = []memory.CandidateSummary{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}}
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	signal, err := c.CheckDigest(context.Background())
	if err != nil {
		t.Fatalf("CheckDigest: %v", err)
	}
	if signal.PendingCount != 3 {
		t.Errorf("CheckDigest: PendingCount = %d, want 3", signal.PendingCount)
	}
}
