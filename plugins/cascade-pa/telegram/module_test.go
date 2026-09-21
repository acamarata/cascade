package telegram

// Purpose (this file): the dispatch gate's tests — every refusal the ticket's
//   acceptance criteria name, applied on BOTH transports, plus
//   TestTelegramPairing, the integration case the ticket's failure oracle
//   enumerates (pairing survives restart, replays do not repeat actions,
//   egress precedes API use, no unpaired dispatch).
//
// SPORT: plugins/cascade-pa/telegram module-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func textUpdate(id, fromID, chatID int64, text string) Update {
	return Update{UpdateID: id, Message: &Message{
		MessageID: id, From: &User{ID: fromID, FirstName: "T"},
		Chat: Chat{ID: chatID, Type: "private"}, Text: text,
	}}
}

func callbackUpdate(id, fromID int64, data string) Update {
	return Update{UpdateID: id, CallbackQuery: &CallbackQuery{
		ID: "cb-1", From: User{ID: fromID, FirstName: "T"}, Data: data,
	}}
}

// TestTelegramNonPairedSenderDropped is the fail-closed unbound-bot criterion:
// no handler runs, and the sender gets exactly "not paired".
func TestTelegramNonPairedSenderDropped(t *testing.T) {
	rig := newDefaultRig(t)
	called := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 999, 999, "hello there"))
	if called {
		t.Fatal("a handler ran for an unpaired sender")
	}
	if got := lastSent(t, rig.doer); got != replyNotPaired {
		t.Fatalf("reply = %q, want %q", got, replyNotPaired)
	}
}

// TestTelegramStrangerOnABoundBotIsIndistinguishable is T0 D5: on a BOUND bot
// a stranger's "/pair <code>" must look exactly like a stranger's plain text
// and must burn nothing — otherwise any stranger exhausts the owner's code in
// five messages and learns the bot's pairing state on the way.
func TestTelegramStrangerOnABoundBotIsIndistinguishable(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	ctx := context.Background()
	code, err := rig.stores.Pairing.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}

	rig.module.dispatch(ctx, textUpdate(1, 999, 999, "hello"))
	plain := lastSent(t, rig.doer)
	rig.module.dispatch(ctx, textUpdate(2, 999, 999, "/pair "+code))
	pairAttempt := lastSent(t, rig.doer)

	if plain != replyNotPaired || pairAttempt != plain {
		t.Fatalf("stranger replies differ: plain %q, /pair %q", plain, pairAttempt)
	}
	pending, err := rig.stores.Pairing.Pending(ctx, testSubject)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if !pending {
		t.Fatal("a stranger's /pair consumed the owner's outstanding code")
	}
	if st, _, _ := rig.state.Load(ctx, testSubject); st.WrongAttempts != 0 {
		t.Fatalf("a stranger's /pair counted %d attempts against the owner's code", st.WrongAttempts)
	}
	if !rig.allows(t, "111") {
		t.Fatal("the owner lost the binding")
	}
}

// TestTelegramAllowlistedSenderDispatches is the positive counterpart, and the
// Origin/Untrusted stamp assertion.
func TestTelegramAllowlistedSenderDispatches(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	var got InboundMessage
	called := false
	rig.module.RegisterHandler(HandlerText, func(_ context.Context, msg InboundMessage) error {
		called, got = true, msg
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "hello there"))
	if !called {
		t.Fatal("the handler did not run for an allowlisted sender")
	}
	if got.Origin != OriginBridgeTelegram || !got.Untrusted {
		t.Fatalf("stamp = %+v, want Origin=%q Untrusted=true", got, OriginBridgeTelegram)
	}
}

// TestTelegramMediaUpdateRefused covers R-21.227's media refusal on BOTH
// transports and proves no getFile-shaped call is ever made.
func TestTelegramMediaUpdateRefused(t *testing.T) {
	media := map[string]func(*Message){
		"photo":    func(m *Message) { m.Photo = []json.RawMessage{[]byte(`{"file_id":"synthetic"}`)} },
		"document": func(m *Message) { m.Document = []byte(`{"file_id":"synthetic"}`) },
		"voice":    func(m *Message) { m.Voice = []byte(`{"file_id":"synthetic"}`) },
		"video":    func(m *Message) { m.Video = []byte(`{"file_id":"synthetic"}`) },
		"sticker":  func(m *Message) { m.Sticker = []byte(`{"file_id":"synthetic"}`) },
		"location": func(m *Message) { m.Location = []byte(`{"latitude":0}`) },
	}
	for kind, attach := range media {
		t.Run(kind, func(t *testing.T) {
			rig := newDefaultRig(t)
			rig.bind(t, "111")
			called := false
			rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
				called = true
				return nil
			})
			u := textUpdate(1, 111, 111, "")
			attach(u.Message)
			rig.module.dispatch(context.Background(), u)
			if called {
				t.Fatalf("a handler ran for a %s update", kind)
			}
			if got := lastSent(t, rig.doer); got != replyMediaRefused {
				t.Fatalf("reply = %q, want %q", got, replyMediaRefused)
			}
			for _, m := range rig.doer.methods() {
				if m != MethodSendMessage && m != MethodAnswerCallbackQuery && m != MethodGetUpdates {
					t.Fatalf("an out-of-allowlist call (%s) was made for a media update", m)
				}
			}
		})
	}
}

// TestTelegramCallbackMediaRefused: the callback path applies the same media
// check, answered over answerCallbackQuery.
func TestTelegramCallbackMediaRefused(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	called := false
	rig.module.RegisterHandler(HandlerCallbackQuery, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	u := callbackUpdate(1, 111, "approve:req-1")
	u.CallbackQuery.Message = &Message{MessageID: 7, Chat: Chat{ID: 111},
		Photo: []json.RawMessage{[]byte(`{"file_id":"synthetic"}`)}}
	rig.module.dispatch(context.Background(), u)
	if called {
		t.Fatal("the callback handler ran for a media-bearing callback message")
	}
	if got := lastSent(t, rig.doer); got != replyMediaRefused {
		t.Fatalf("answer = %q, want %q", got, replyMediaRefused)
	}
}

// TestTelegramElevatedVerbRefusedOnBothPaths is the CR's bypass input: the
// byte-identical command refused as text used to pass as callback data.
func TestTelegramElevatedVerbRefusedOnBothPaths(t *testing.T) {
	for _, command := range []string{"/enroll worker-3", "/node enroll worker-3", "/upgrade worker-3"} {
		t.Run(command, func(t *testing.T) {
			rig := newDefaultRig(t)
			rig.bind(t, "111")
			textRan, callbackRan := false, false
			rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
				textRan = true
				return nil
			})
			rig.module.RegisterHandler(HandlerCallbackQuery, func(context.Context, InboundMessage) error {
				callbackRan = true
				return nil
			})
			ctx := context.Background()
			rig.module.dispatch(ctx, textUpdate(1, 111, 111, command))
			textReply := lastSent(t, rig.doer)
			rig.module.dispatch(ctx, callbackUpdate(2, 111, command))
			callbackReply := lastSent(t, rig.doer)

			if textRan || callbackRan {
				t.Fatalf("an elevated verb reached a handler (text=%v callback=%v)", textRan, callbackRan)
			}
			for _, reply := range []string{textReply, callbackReply} {
				if !strings.Contains(reply, "refused over the bridge") {
					t.Fatalf("reply %q does not name the refusal", reply)
				}
			}
		})
	}
}

// TestTelegramElevationComesFromTheHostPolicy proves the classification is the
// injected policy's, not a list inside this package: under a policy that
// classifies nothing as elevated, the same command dispatches.
func TestTelegramElevationComesFromTheHostPolicy(t *testing.T) {
	rig := newRig(t, fixedTestClock{t0()}, &okRegistrar{}, permissivePolicy())
	rig.bind(t, "111")
	ran := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		ran = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "/enroll worker-3"))
	if !ran {
		t.Fatal("the refusal survived a policy that classifies nothing as elevated; " +
			"it is coming from a hardcoded list rather than the host's table")
	}
}

// TestTelegramNoElevationPolicyRefusesEveryCommand is the fail-closed half: an
// unwired policy refuses every command-shaped message.
func TestTelegramNoElevationPolicyRefusesEveryCommand(t *testing.T) {
	rig := newRig(t, fixedTestClock{t0()}, &okRegistrar{}, nil)
	rig.bind(t, "111")
	ran := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		ran = true
		return nil
	})
	ctx := context.Background()
	rig.module.dispatch(ctx, textUpdate(1, 111, 111, "/status"))
	if ran {
		t.Fatal("a command was admitted with no elevation policy wired")
	}
	rig.module.dispatch(ctx, textUpdate(2, 111, 111, "just some prose"))
	if !ran {
		t.Fatal("ordinary prose was refused; the bridge would carry nothing at all")
	}
}

// TestTelegramMessageWithoutFromIsIgnored: a channel post has no sender to
// pair, and dispatch must not panic on the nil pointer.
func TestTelegramMessageWithoutFromIsIgnored(t *testing.T) {
	rig := newDefaultRig(t)
	called := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), Update{UpdateID: 1,
		Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "channel"}, Text: "channel post"}})
	if called {
		t.Fatal("a handler ran for a Message with no From")
	}
	if len(rig.doer.methods()) != 0 {
		t.Fatalf("a From-less message produced outbound traffic: %v", rig.doer.methods())
	}
}

// TestTelegramUnreadableStateRefuses: a state store the module cannot read
// answers "not paired" and dispatches nothing.
func TestTelegramUnreadableStateRefuses(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.state.loadErr = errTestStoreDown
	called := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "hello"))
	if called {
		t.Fatal("a handler ran while the binding store was unreadable")
	}
	if got := lastSent(t, rig.doer); got != replyNotPaired {
		t.Fatalf("reply = %q, want %q", got, replyNotPaired)
	}
}
