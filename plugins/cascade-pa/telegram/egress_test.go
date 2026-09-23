// Purpose (this file): unit coverage for egress.go's pure helpers
//   (thread-id mapping, refusal reason text) and its three fail-closed
//   defaults, plus the fakes chat_test.go drives TelegramBridge through
//   (fakeThreadPrivacy, fakeChatService, fakeDivergenceSink).
//
// SPORT: plugins/cascade-pa/telegram egress-helpers/TEST (P1-E23-W5-S48-T2).

package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeThreadPrivacy is a ThreadPrivacyResolver test double: absence reads
// restricted, mirroring internal/conversation/privacy.go's real "absence
// means restricted" contract, so a test that never calls set() exercises
// the same fail-closed shape production has.
type fakeThreadPrivacy struct {
	mu    sync.Mutex
	tiers map[string]cascadepa.SensitivityTier
	err   error
}

func newFakeThreadPrivacy() *fakeThreadPrivacy {
	return &fakeThreadPrivacy{tiers: map[string]cascadepa.SensitivityTier{}}
}

func (f *fakeThreadPrivacy) set(threadID string, tier cascadepa.SensitivityTier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tiers[threadID] = tier
}

func (f *fakeThreadPrivacy) ThreadPrivacy(_ context.Context, threadID string) (cascadepa.SensitivityTier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if tier, ok := f.tiers[threadID]; ok {
		return tier, nil
	}
	return cascadepa.TierRestricted, nil
}

// appendTurnCall records one fakeChatService.AppendTurn invocation.
type appendTurnCall struct{ threadID, role, text string }

// fakeChatService is a ChatService test double. block, when non-nil, is
// waited on BEFORE the call proceeds — a test's way to hold a
// handleInbound call in flight long enough to prove Drain actually waits.
type fakeChatService struct {
	mu         sync.Mutex
	calls      []appendTurnCall
	err        error
	nextTurnID string
	block      chan struct{}
}

func (f *fakeChatService) AppendTurn(_ context.Context, threadID, role, text string) (string, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, appendTurnCall{threadID: threadID, role: role, text: text})
	if f.nextTurnID == "" {
		return "turn-1", nil
	}
	return f.nextTurnID, nil
}

func (f *fakeChatService) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeDivergenceSink is a DivergenceSink test double.
type fakeDivergenceSink struct {
	mu     sync.Mutex
	events []RefusalEvent
}

func (f *fakeDivergenceSink) EmitRefused(_ context.Context, e RefusalEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeDivergenceSink) snapshot() []RefusalEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RefusalEvent, len(f.events))
	copy(out, f.events)
	return out
}

func TestThreadIDForChat_RoundTrip(t *testing.T) {
	threadID := threadIDForChat(9001)
	if threadID != "bridge-telegram:9001" {
		t.Fatalf("threadIDForChat(9001) = %q, want %q", threadID, "bridge-telegram:9001")
	}
	chatID, ok := chatIDForThread(threadID)
	if !ok || chatID != 9001 {
		t.Fatalf("chatIDForThread(%q) = (%d, %v), want (9001, true)", threadID, chatID, ok)
	}
}

func TestChatIDForThread_ForeignIDRefuses(t *testing.T) {
	cases := []string{"", "bridge-whatsapp:9001", "9001", "bridge-telegram:not-a-number"}
	for _, threadID := range cases {
		if _, ok := chatIDForThread(threadID); ok {
			t.Errorf("chatIDForThread(%q): got ok=true, want false", threadID)
		}
	}
}

func TestRefusalReasonForTier(t *testing.T) {
	cases := []struct {
		tier cascadepa.SensitivityTier
		want string
	}{
		{cascadepa.TierRestricted, reasonThreadRestricted},
		{cascadepa.TierLocalOnly, reasonThreadLocalOnly},
		// TierInternal/TierPublic ARE admitted by the bridge class: a
		// Guard refusal at one of these tiers is not a privacy exclusion,
		// so it gets the generic unavailable text, never "local-only".
		{cascadepa.TierInternal, reasonBridgeUnavailable},
		{cascadepa.TierPublic, reasonBridgeUnavailable},
		// An unresolvable tier (§5.16) is mapped to TierLocalOnly by the
		// caller before this function ever runs; any other unrecognized
		// value falls through to the same fail-closed local-only text.
		{cascadepa.SensitivityTier("unresolvable"), reasonThreadLocalOnly},
	}
	for _, c := range cases {
		if got := refusalReasonForTier(c.tier); got != c.want {
			t.Errorf("refusalReasonForTier(%q) = %q, want %q", c.tier, got, c.want)
		}
	}
}

func TestUnconfiguredThreadPrivacyResolver_Refuses(t *testing.T) {
	tier, err := (unconfiguredThreadPrivacyResolver{}).ThreadPrivacy(context.Background(), "any")
	if tier != cascadepa.TierLocalOnly {
		t.Errorf("tier = %q, want TierLocalOnly", tier)
	}
	// Identity, not errors.Is: pkg/cascade's Is compares Kind only, so
	// errors.Is would hold for any KindUnavailable error in the tree and
	// this assertion could never fail.
	if err != errNoThreadPrivacyResolver {
		t.Errorf("err = %v, want errNoThreadPrivacyResolver", err)
	}
	if r := threadPrivacyOrRefuseAll(nil); r == nil {
		t.Fatal("threadPrivacyOrRefuseAll(nil) returned nil")
	}
}

func TestUnconfiguredChatService_Refuses(t *testing.T) {
	turnID, err := (unconfiguredChatService{}).AppendTurn(context.Background(), "t", "user", "hi")
	if turnID != "" {
		t.Errorf("turnID = %q, want empty", turnID)
	}
	if err != errNoChatService {
		t.Errorf("err = %v, want errNoChatService", err)
	}
	if c := chatServiceOrRefuseAll(nil); c == nil {
		t.Fatal("chatServiceOrRefuseAll(nil) returned nil")
	}
}

func TestDiscardDivergenceSink_NeverPanics(t *testing.T) {
	// The only assertion a discard sink admits: calling it does not panic
	// and divergenceOrDiscard(nil) never returns nil.
	(discardDivergenceSink{}).EmitRefused(context.Background(), RefusalEvent{})
	if d := divergenceOrDiscard(nil); d == nil {
		t.Fatal("divergenceOrDiscard(nil) returned nil")
	}
}

// TestChatBridge_ForwardRefusesUnmarkedUntrusted proves R-21.227's marker is
// pinned, not propagated: handleInbound is called directly (bypassing
// dispatch, whose only constructor stampInbound always sets Untrusted=true)
// with a hand-built InboundMessage carrying Untrusted=false, and forward
// must refuse before AppendTurn or any reply.
func TestChatBridge_ForwardRefusesUnmarkedUntrusted(t *testing.T) {
	r := newBridgeRig(t)
	threadID := threadIDForChat(9011)
	r.privacy.set(threadID, cascadepa.TierInternal)
	msg := InboundMessage{Update: textUpdate(1, 5560, 9011, "should not be recorded"),
		Origin: OriginBridgeTelegram, Untrusted: false}

	err := r.bridge.handleInbound(context.Background(), msg)

	if err == nil || !cascade.HasKind(err, cascade.KindIntegrity) ||
		!strings.Contains(err.Error(), "untrusted=false") {
		t.Fatalf("handleInbound = %v, want a KindIntegrity refusal naming untrusted=false", err)
	}
	if n := r.chat.callCount(); n != 0 {
		t.Fatalf("AppendTurn called %d times, want 0", n)
	}
}

// TestFixture_SendMessageResponseDecodes is Art.2's external-contract
// fixture: sendmessage_ok.json through the REAL production decode path
// (newAPIDoer/httpDoer.Do -> decodeEnvelope), the identical call
// BotClient.SendMessage makes, over a recordingPoster fake transport
// (apicall_test.go) so no net/http import reaches this untagged test file.
func TestFixture_SendMessageResponseDecodes(t *testing.T) {
	post := &recordingPoster{reply: mustReadTestdata(t, "sendmessage_ok.json")}
	d := newAPIDoer(syntheticToken, post)
	var out Message
	if err := d.Do(context.Background(), MethodSendMessage,
		sendMessageParams{ChatID: 555000111, Text: "hi"}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Chat.ID != 555000111 || out.From == nil || !out.From.IsBot {
		t.Fatalf("decoded %+v, want the fixture's bot-authored reply", out)
	}
}
