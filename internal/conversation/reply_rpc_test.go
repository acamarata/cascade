package conversation

// Purpose: chat.reply_open / chat.reply refuse every bad request before any
//   dispatch, bind a stream to its caller and thread for one use, keep a
//   principal to its own scope, never put reply text on the events bus, and
//   import no provider package and no internal/daemon (R17 M-1).

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/provider"
)

// seedScopedThread creates id attributed to scope with one user turn.
func seedScopedThread(t *testing.T, store Store, id, scope string) Turn {
	t.Helper()
	if err := store.CreateThread(context.Background(), id, provider.SensitivityInternal, scope, 1); err != nil {
		t.Fatal(err)
	}
	return seedReplyTurn(t, store, id, RoleUser, OriginLocal, "", "question in "+id)
}

func TestReplyValidatesRequest(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("x"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	seedOwnedThread(t, f.store, "other", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "q")
	assistant := seedReplyTurn(t, f.store, "th", RoleAssistant, OriginLocal, "", "a")
	foreign := seedReplyTurn(t, f.store, "other", RoleUser, OriginLocal, "", "elsewhere")
	ctx := ownerCLICtx()
	cases := []struct {
		name, thread, turn string
		want               error
	}{
		{"unknown thread", "nope", user.ID, errReplyThreadNotFound},
		{"unknown turn", "th", "missing", errReplyTurnNotFound},
		{"turn outside thread", "th", foreign.ID, errReplyTurnOutside},
		{"non-user turn", "th", assistant.ID, errReplyNotUserTurn},
		{"empty ids", "", "", errReplyParams},
	}
	for _, c := range cases {
		_, err := f.replier.open(ctx, c.thread, c.turn)
		wantSentinel(t, err, c.want)
	}
	_, err := f.replier.Reply(ctx, ReplyRequest{StreamID: "00112233445566778899aabbccddeeff"})
	wantSentinel(t, err, errStreamNotFound)
	expiring, err := f.replier.open(ctx, "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(replyStreamTTL)
	_, err = f.replier.Reply(ctx, ReplyRequest{StreamID: expiring})
	wantSentinel(t, err, errStreamNotFound)
	_, err = f.replier.Reply(context.Background(), ReplyRequest{StreamID: expiring})
	wantSentinel(t, err, errReplyNoCaller)
	_, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: MethodReply,
		Params: json.RawMessage(`{"stream_id":"` + expiring + `","thread_id":"th"}`)})
	wantRPCError(t, eo, errReplyParams)
	if n := len(f.door.snapshot()); n != 0 {
		t.Fatalf("%d door calls, want none before validation passes", n)
	}
}

func TestReplyRefusesForeignScopePrincipal(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("ok"), 4000)
	alpha := seedScopedThread(t, f.store, "alpha-th", "project:alpha")
	seedOwnedThread(t, f.store, "owner-th", provider.SensitivityRestricted)
	ownerTurn := seedReplyTurn(t, f.store, "owner-th", RoleUser, OriginLocal, "", "mine")
	beta := principalCtx("p-beta", "project:beta")
	_, err := f.replier.open(beta, "alpha-th", alpha.ID)
	wantSentinel(t, err, errScopeDenied)
	_, err = f.replier.open(beta, "owner-th", ownerTurn.ID)
	wantSentinel(t, err, errScopeDenied)
	_, eo := f.reg.Dispatch(beta, &rpc.Request{JSONRPC: "2.0", Method: MethodReplyOpen,
		Params: json.RawMessage(`{"thread_id":"alpha-th","turn_id":"` + alpha.ID + `"}`)})
	wantRPCError(t, eo, errScopeDenied)
	if _, eo := f.reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: MethodReplyOpen,
		Params: json.RawMessage(`{}`)}); eo == nil {
		t.Fatal("chat.reply_open with no caller was admitted")
	}
	if n := len(f.door.snapshot()); n != 0 {
		t.Fatalf("%d door calls after refusals, want 0", n)
	}
	res, err := f.openAndReply(t, principalCtx("p-alpha", "project:alpha"), "alpha-th", alpha.ID)
	if err != nil || res.ThreadID != "alpha-th" {
		t.Fatalf("own-scope reply = %+v, %v", res, err)
	}
}

func TestReplyStreamIDBoundToCallerAndThread(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("ok"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "q")
	a, err := f.replier.open(ownerCLICtx(), "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := f.replier.open(ownerCLICtx(), "th", user.ID)
	if len(a) != 32 || a == b {
		t.Fatalf("stream ids %q %q, want distinct 128-bit hex", a, b)
	}
	if _, err := strconv.ParseUint(a[:16], 16, 64); err != nil {
		t.Fatalf("stream id %q is not hex", a)
	}
	ownerView := principal.WithCaller(context.Background(), principal.Caller{})
	for _, ctx := range []context.Context{principalCtx("p-x", "project:x"), ownerView} {
		_, err := f.replier.Reply(ctx, ReplyRequest{StreamID: a})
		wantSentinel(t, err, errStreamForeign)
	}
	res, err := f.replier.Reply(ownerCLICtx(), ReplyRequest{StreamID: a})
	if err != nil || res.ThreadID != "th" || res.TurnID != user.ID {
		t.Fatalf("bound caller's reply = %+v, %v; want the bound thread and turn", res, err)
	}
}

func TestReplyStreamIDOneUse(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("ok"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "q")
	id, err := f.replier.open(ownerCLICtx(), "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replier.Reply(ownerCLICtx(), ReplyRequest{StreamID: id}); err != nil {
		t.Fatal(err)
	}
	_, err = f.replier.Reply(ownerCLICtx(), ReplyRequest{StreamID: id})
	wantSentinel(t, err, errStreamUsed)
	_, eo := f.reg.Dispatch(ownerCLICtx(), &rpc.Request{JSONRPC: "2.0", Method: MethodReply,
		Params: json.RawMessage(`{"stream_id":"` + id + `"}`)})
	wantRPCError(t, eo, errStreamUsed)
}

func TestReplyDeltasNeverInEventLog(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("SECRETREPLY", "TOKEN"), 4000)
	bus := &fakeBus{}
	reg := rpc.NewRegistry()
	NewAdapter(f.store, bus, passthroughSubst{}, newAdapterTestClock(), "").RegisterHandlers(reg)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	appended, eo := dispatch(t, reg, MethodAppendTurn, appendTurnParams{ThreadID: "th", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "visible user text"}}})
	if eo != nil {
		t.Fatalf("append: %+v", eo)
	}
	res, err := f.openAndReply(t, ownerCLICtx(), "th", appended.(appendTurnResult).TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bus.published) == 0 || !strings.Contains(string(bus.published[0]), "visible user text") {
		t.Fatal("the bus log is empty; the probe would pass vacuously")
	}
	for _, payload := range bus.published {
		if strings.Contains(string(payload), "SECRETREPLY") || strings.Contains(string(payload), res.AssistantTurnID) {
			t.Fatalf("reply content reached the events bus: %s", payload)
		}
	}
}

func TestReplyImportsOnlyTheDoor(t *testing.T) {
	files, err := filepath.Glob("reply*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "/providers/") || strings.HasSuffix(path, "/providers") ||
				strings.Contains(path, "/internal/daemon") || strings.HasPrefix(path, "net/http") {
				t.Fatalf("%s imports %s: every model call must go through the injected ReplyDoor", name, path)
			}
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("parsed %d reply*.go files, want the five non-test files", checked)
	}
}

func TestPrincipalReplyStreamSeesOnlyOwnThread(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("for alpha"), 4000)
	alpha := seedScopedThread(t, f.store, "alpha-th", "project:alpha")
	seedScopedThread(t, f.store, "beta-th", "project:beta")
	pa, pb := principalCtx("p-alpha", "project:alpha"), principalCtx("p-beta", "project:beta")
	id, err := f.replier.open(pa, "alpha-th", alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	callerB, _ := principal.CallerFrom(pb)
	_, err = f.hub.Subscribe(context.Background(), id, callerB)
	wantSentinel(t, err, errStreamForeign)
	_, err = f.replier.Reply(pb, ReplyRequest{StreamID: id})
	wantSentinel(t, err, errStreamForeign)
	callerA, _ := principal.CallerFrom(pa)
	frames, err := f.hub.Subscribe(context.Background(), id, callerA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replier.Reply(pa, ReplyRequest{StreamID: id}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for fr := range frames {
		n++
		if fr.ThreadID != "alpha-th" {
			t.Fatalf("frame %+v carries another thread", fr)
		}
	}
	if n < 2 {
		t.Fatalf("%d frames, want the delta and the terminal frame", n)
	}
}
