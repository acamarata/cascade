package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func localOnlyCallers() map[string]context.Context {
	return map[string]context.Context{
		"owner": ownerCLICtx(), "no-caller": context.Background(),
		"in-process": principal.WithCaller(context.Background(), principal.Caller{}),
		"principal":  principalCtx("p-a", "project:a"),
		"principal-marker": principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true,
			Principal: &principal.Principal{ID: "p-a", Scope: "project:a"}}),
	}
}

// TestLocalOnlyServedOnlyToOwnerCLIMarker drives each read's actual handler.
func TestLocalOnlyServedOnlyToOwnerCLIMarker(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateThread(context.Background(), "local", provider.SensitivityLocalOnly, "project:a", 1); err != nil {
		t.Fatal(err)
	}
	seedReplyTurn(t, store, "local", RoleUser, OriginLocal, "", "beacon private content")
	a := NewAdapter(store, nil, nil, newTestClock(), "")
	for name, ctx := range localOnlyCallers() {
		t.Run(name, func(t *testing.T) { assertLocalReads(ctx, t, a, name == "owner") })
	}
	reg := rpc.NewRegistry()
	a.RegisterHandlers(reg)
	for _, verb := range localReadRequests() {
		res, eo := reg.Dispatch(context.Background(), &rpc.Request{Method: verb.method, Params: verb.params})
		assertLocalRead(t, verb.method, res, eo != nil, false)
	}
}

func localReadRequests() []struct {
	method string
	params json.RawMessage
} {
	return []struct {
		method string
		params json.RawMessage
	}{
		{MethodListThreads, json.RawMessage(`{}`)},
		{MethodGetThread, json.RawMessage(`{"thread_id":"local"}`)},
		{MethodSearch, json.RawMessage(`{"query":"beacon"}`)},
	}
}

func assertLocalReads(ctx context.Context, t *testing.T, a *Adapter, owner bool) {
	t.Helper()
	for i, verb := range localReadRequests() {
		handler := []rpc.HandlerFunc{a.scopedListThreads, a.scopedGetThread, a.scopedSearch}[i]
		res, err := handler(ctx, verb.params)
		if err != nil && !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("%s: %v", verb.method, err)
		}
		assertLocalRead(t, verb.method, res, err != nil, owner)
	}
}

func assertLocalRead(t *testing.T, method string, res any, refused, owner bool) {
	t.Helper()
	if refused != (method == MethodGetThread && !owner) {
		t.Fatalf("%s refused=%v owner=%v", method, refused, owner)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "local") != owner {
		t.Fatalf("%s owner=%v: %s", method, owner, raw)
	}
}

// TestResumeLocalOnlyRefusedWithoutOwnerMarker checks actual contributing threads.
func TestResumeLocalOnlyRefusedWithoutOwnerMarker(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"local", "destination"} {
		if err := store.CreateThread(context.Background(), id, provider.SensitivityLocalOnly, "project:a", 1); err != nil {
			t.Fatal(err)
		}
	}
	seedReplyTurn(t, store, "local", RoleUser, OriginLocal, "", "private resume beacon")
	querier := rpc.NewRegistry()
	querier.Register(recallWhatMethod, func(context.Context, json.RawMessage) (any, error) {
		return recallWhatResponseWire{Results: []recallWhatResultWire{{Domain: "conversation", Path: "local"}}}, nil
	})
	resumer, err := NewResumer(store, echoComposer{}, querier, &recordingSubst{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewAdapter(store, nil, nil, newTestClock(), "")
	a.SetResume(resumer)
	for name, ctx := range localOnlyCallers() {
		for _, source := range []resumeParams{{TopicID: "local"}, {Query: "beacon"}} {
			source.SessionThreadID = "destination"
			raw, _ := json.Marshal(source)
			res, err := a.handleResume(ctx, raw)
			if name != "owner" {
				wantSentinel(t, err, errResumeLocalOnly)
				if res != nil {
					t.Fatalf("refused resume returned %+v", res)
				}
			} else if err != nil || !strings.Contains(res.(resumeResult).LeadingContext, "private resume beacon") {
				t.Fatalf("owner resume = %+v, %v", res, err)
			}
		}
	}
	if got := len(storedTurns(t, store, "local")); got != 1 {
		t.Fatalf("resume wrote turns: %d", got)
	}
	if got := len(storedTurns(t, store, "destination")); got != 0 {
		t.Fatalf("resume wrote destination turns: %d", got)
	}
}

// TestReplyOpenLocalOnlyRefusedWithoutOwnerMarker checks streams and door calls.
func TestReplyOpenLocalOnlyRefusedWithoutOwnerMarker(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("owner reply"), 4000)
	if err := f.store.CreateThread(context.Background(), "local", provider.SensitivityLocalOnly, "project:a", 1); err != nil {
		t.Fatal(err)
	}
	user := seedReplyTurn(t, f.store, "local", RoleUser, OriginLocal, "", "question")
	for name, ctx := range localOnlyCallers() {
		if name == "owner" {
			continue
		}
		before := len(f.hub.streams)
		id, err := f.replier.open(ctx, "local", user.ID)
		want := error(errReplyLocalOnly)
		if name == "no-caller" {
			want = errReplyNoCaller
		}
		wantSentinel(t, err, want)
		if id != "" || len(f.hub.streams) != before {
			t.Fatalf("%s open = %q, %v", name, id, err)
		}
		assertLocalReplyRefused(ctx, t, f, user.ID, want)
	}
	if len(f.door.snapshot()) != 0 || len(storedTurns(t, f.store, "local")) != 1 {
		t.Fatal("refused reply called door or wrote a turn")
	}
	res, err := f.openAndReply(t, ownerCLICtx(), "local", user.ID)
	if err != nil || res.AssistantTurnID == "" || f.door.streamCalls() != 1 {
		t.Fatalf("owner reply = %+v, %v", res, err)
	}
}

func assertLocalReplyRefused(ctx context.Context, t *testing.T, f *replyFixture, turnID string, want error) {
	t.Helper()
	var id string
	// A preexisting binding forces chat.reply to run its own target check.
	if caller, ok := principal.CallerFrom(ctx); ok {
		var err error
		id, err = f.hub.open("local", turnID, caller)
		if err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.replier.Reply(ctx, ReplyRequest{StreamID: id})
	wantSentinel(t, err, want)
	if res != (ReplyResult{}) {
		t.Fatalf("refused reply = %+v", res)
	}
}
