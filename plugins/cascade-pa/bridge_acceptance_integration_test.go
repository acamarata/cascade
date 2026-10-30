//go:build integration

// Purpose: P1-CPA-14 acceptance [5], the owner-window leg. A real
//   cascadepa.ChatBridge (telegram.NewModule's one front door) is paired by
//   a locally printed one-use code, bound through the real chat-to-thread
//   map to a thread chat.create_thread minted, sent to through bridge.send
//   (the cascade_cpa_send route) and answered by the owner's nonce reply.
//
// Constraints: runs only with CASCADE_TG_OWNER_WINDOW=1 and the vault token
//   mapped onto CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN, in the P1-EXIT-11
//   owner-present session; otherwise it skips naming the variable. Only the
//   DM chat bound by redeeming the printed code is addressed; no channel id
//   is read. No bot id, username, chat digits or wire body in any log or
//   failure message.
//
// SPORT: plugins/cascade-pa TestPABridge_OwnerWindow/TEST (P1-CPA-14).

package cascadepa_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// pairingWaitTimeout bounds how long this test waits for the owner's own
// "/pair <code>" DM to land and bind, once the code is printed.
const pairingWaitTimeout = 5 * time.Minute

// pairingPollInterval is how often this test re-checks stores.Binding.Bound
// while waiting for the owner's pairing DM.
const pairingPollInterval = 2 * time.Second

// replyWaitTimeout is R10's own figure for the TG->CLI reply leg.
const replyWaitTimeout = 10 * time.Minute

// ccToTGRig bundles TestPABridge_OwnerWindow's own collaborators — split
// out of the test body so ccToTGSetup and the test function each stay
// under Art.10.3's 50-line function cap.
type ccToTGRig struct {
	token      string
	subject    string
	rt         *recordingRoundTripper
	httpClient *http.Client
	stores     *cascadepa.Stores
	state      *integrationState
	convStore  conversation.Store
	chatSvc    *integrationChatService
	threads    telegram.ThreadMap
	bridge     *telegram.TelegramBridge
}

// ccToTGSetup builds the real module/bridge and confirms the bot's live
// identity — TestPABridge_OwnerWindow's own preamble, split out for the
// 50-line cap.
func ccToTGSetup(ctx context.Context, t *testing.T) *ccToTGRig {
	t.Helper()
	token := integrationCredentials(t)
	subject := telegram.SubjectFromToken(token)
	rt := &recordingRoundTripper{}
	httpClient := &http.Client{Transport: rt}

	var me telegram.User
	acceptGetMe(ctx, t, token, httpClient, &me)
	if me.Username == "" || !me.IsBot {
		t.Fatal("getMe did not return a usable bot identity")
	}

	state := newIntegrationState()
	pairKey, err := cascadepa.DerivePairCodeKey(token)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	stores := cascadepa.NewStores(fixedIntegrationClock{}, integrationRegistrar{}, state, pairKey)
	egressGate, scanner := newIntegrationFirewall(t)
	chatSvc := &integrationChatService{}
	convStore := newIntegrationConversationStore(t)
	module := telegram.NewModule(token, httpClient, egressGate, cascadepa.ElevationOrRefuseAll(nil),
		stores, nil, scanner, nil)
	threads := telegram.NewThreadMap(cascadepa.NewThreadMapStore(state), stores.Binding, subject)
	bridge := telegram.NewTelegramBridge(module, stores.Binding, subject, telegram.BridgeSeams{
		Chat: chatSvc, Privacy: integrationRealPrivacy{store: convStore}, Divergence: integrationDivergence{},
		Threads: threads, Legacy: integrationNoLegacy{},
	})
	return &ccToTGRig{token: token, subject: subject, rt: rt, httpClient: httpClient, stores: stores, state: state,
		convStore: convStore, chatSvc: chatSvc, threads: threads, bridge: bridge}
}

// ownerWindowVar opts in to the owner-present run (P1-EXIT-11 only).
const ownerWindowVar = "CASCADE_TG_OWNER_WINDOW"

// TestPABridge_OwnerWindow is the owner-present round trip, in order: /pair
// with a locally printed one-use code, an internal thread from
// chat.create_thread bound to the paired DM through the real map, a
// bridge.send (the cascade_cpa_send route) mirrored to Telegram, then a
// nonce reply within 10 minutes appended to that thread. The approve and
// reject taps and the raw-JSON fixtures are TestTelegramLiveCapture's leg.
func TestPABridge_OwnerWindow(t *testing.T) {
	if os.Getenv(ownerWindowVar) != "1" {
		t.Skipf("owner window: %s is not 1 (P1-EXIT-11 owner-present session only)", ownerWindowVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairingWaitTimeout+replyWaitTimeout+time.Minute)
	defer cancel()
	rig := ccToTGSetup(ctx, t)
	primedOffset := primeOffsetPastBacklog(ctx, t, rig.token, rig.httpClient)
	primeLedgerOffset(ctx, t, rig.state, rig.subject, primedOffset)
	code, err := rig.stores.Pairing.IssueCode(ctx, rand.Reader, rig.subject)
	if err != nil {
		t.Fatalf("IssueCode (real pairing path): %v", redact(err.Error(), rig.token))
	}
	t.Logf("PAIRING CODE (one use, valid 10 min, never sent to Telegram by this test): %s", code)
	t.Logf("ACTION NEEDED: open a DIRECT MESSAGE with the bot and %s", fmtPairInstruction(code))
	if err := rig.bridge.Start(ctx); err != nil {
		t.Fatalf("Start (real long-poll): %v", redact(err.Error(), rig.token))
	}
	defer func() { _ = rig.bridge.Stop(context.Background()) }()

	threadID := bindOwnerThread(ctx, t, rig, waitForRealPairing(ctx, t, rig.stores, rig.state, rig.subject))
	plugins.WireChatBridge(&plugins.BridgeRuntime{ChatBridge: rig.bridge}, ownerWindowClock{})
	defer func() { _ = plugins.UnwireChatBridge(context.Background()) }()

	marker := acceptMarker(t)
	bridgeSendMirrored(ctx, t, threadID, "[cascade acceptance] "+marker)
	assertSendCaptured(t, rig.rt.snapshot(), marker)
	runReplyRoundtrip(ctx, t, rig.bridge, rig.chatSvc, threadID)
}

// bindOwnerThread mints an internal thread through chat.create_thread and
// binds the paired owner's DM to it through the real chat-to-thread map.
func bindOwnerThread(ctx context.Context, t *testing.T, rig *ccToTGRig, ownerSenderID string) string {
	t.Helper()
	owner, err := strconv.ParseInt(ownerSenderID, 10, 64)
	if err != nil {
		t.Fatal("the bound sender id is not numeric")
	}
	minted, err := rig.chatSvc.CreateThread(ctx, "internal")
	mustNot(t, "chat.create_thread", err)
	threadID, err := rig.threads.Bind(ctx, telegram.Chat{ID: owner, Type: "private"}, owner, minted)
	mustNot(t, "ThreadMap.Bind", err)
	markIntegrationThreadPrivacy(t, rig.convStore, threadID, cascadepa.TierInternal)
	return threadID
}

// bridgeSendMirrored sends content through bridge.send, the route
// cascade_cpa_send takes, and requires it mirrored.
func bridgeSendMirrored(ctx context.Context, t *testing.T, threadID, content string) {
	t.Helper()
	res, err := plugins.BridgeSend(ctx, plugins.BridgeSendParams{ThreadID: threadID, Content: content, Sensitivity: "internal"})
	if err != nil {
		t.Fatalf("bridge.send: kind %v", errKindOf(err))
	}
	if !res.Mirrored {
		t.Fatalf("bridge.send was not mirrored (refused=%v reason=%q)", res.Refused, res.Reason)
	}
}

// ownerWindowClock is the injected clock bridge.send stamps refusals with.
type ownerWindowClock struct{}

func (ownerWindowClock) Now() time.Time { return time.Now().UTC() }

// errKindOf reports only an error's Kind, never its text.
func errKindOf(err error) cascade.Kind {
	k, _ := cascade.KindOf(err)
	return k
}

// assertSendCaptured proves R5's "capture sendMessage's result ... assert
// message_id and echoed text == body" over the recordingRoundTripper's own
// observation of the REAL response, never the SDK's discarded return
// value.
func assertSendCaptured(t *testing.T, sent capturedSend, marker string) {
	t.Helper()
	if !sent.captured {
		t.Fatal("the recording RoundTripper never observed a sendMessage call")
	}
	if !strings.Contains(string(sent.requestBody), marker) {
		t.Fatal("the request body sent to the Bot API does not carry the marker")
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64  `json:"message_id"`
			Text      string `json:"text"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sent.responseBody, &envelope); err != nil {
		t.Fatal("the captured sendMessage response does not decode")
	}
	if !envelope.OK {
		t.Fatal("sendMessage response ok=false")
	}
	if envelope.Result.MessageID == 0 {
		t.Fatal("sendMessage response carries no message_id")
	}
	if !strings.Contains(envelope.Result.Text, marker) {
		t.Fatal("sendMessage echoed text does not carry the marker")
	}
}

// waitForRealPairing polls the REAL BindingStore -- never a test-side
// Bind call (this file's header) -- until subject is bound, or fails after
// pairingWaitTimeout. It returns the bound sender id read back from the
// SAME durable state row the real pairCoordinator.bind wrote, never an id
// this test invented.
func waitForRealPairing(ctx context.Context, t *testing.T, stores *cascadepa.Stores, state *integrationState, subject string) string {
	t.Helper()
	deadline := time.Now().Add(pairingWaitTimeout)
	for {
		bound, err := stores.Binding.Bound(ctx, subject)
		if err != nil {
			t.Fatalf("Binding.Bound: %v", err)
		}
		if bound {
			st, ok, lerr := state.Load(ctx, subject)
			if lerr != nil || !ok || len(st.AllowedFrom) == 0 {
				t.Fatalf("subject reports bound but its state carries no allowlisted sender (ok=%v err=%v)", ok, lerr)
			}
			t.Log("real pairing bound: the owner's \"/pair\" DM was verified and admitted by the production dispatch chain")
			return st.AllowedFrom[len(st.AllowedFrom)-1]
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatal("no \"/pair <code>\" DM was verified within the pairing window; the printed code above was never redeemed")
		}
		time.Sleep(pairingPollInterval)
	}
}

// runReplyRoundtrip/assertReplyAppended (TestPABridge_OwnerWindow's TG->CLI
// half) live in the sibling file bridge_acceptance_integration_reply_test.go
// -- split out to stay under Art.10.3's 300-line file cap.
