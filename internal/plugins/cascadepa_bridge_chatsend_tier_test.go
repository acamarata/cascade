package plugins

// Purpose (this file): proves the thread-tier half of bridge.send's AND gate
//   over a REAL enabled bridge (the real TelegramBridge.Send, its real
//   binding store and thread-privacy resolver), not a recording fake: the
//   turn's caller-declared sensitivity only NARROWS, it can never widen what
//   the thread's own tier allows. A restricted thread with caller sensitivity
//   "public" must refuse, with zero transport calls and exactly one
//   content-free bridge.refused; the same bound chat at the internal tier
//   delivers exactly one send, so the refusal is the tier and not a broken
//   fixture. Stub-passing input: a gate that reads only the caller-declared
//   sensitivity (the "public" case passes it and must still refuse).
//
// Constraints: synthetic token and chat id, file-only vault, a recording
//   telegram.Poster answering sendMessage from the captured fixture
//   (plugins/cascade-pa/telegram/testdata/README.md), no network. Windows
//   tier-2 refuses at Send's platform gate before the tier decision, so the
//   test skips there.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-wiring/TEST — P1-CPA-01.

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

const (
	tierTestChat   = "555000222"
	tierTestThread = "bridge-telegram:" + tierTestChat
)

// sendRecorder is a telegram.Poster that records sendMessage bodies.
type sendRecorder struct {
	mu    sync.Mutex
	sends []string
	reply []byte
}

func (r *sendRecorder) Post(_ context.Context, url string, body []byte) ([]byte, error) {
	if !strings.HasSuffix(url, "/"+telegram.MethodSendMessage) {
		return nil, cascade.New(cascade.KindInternal, "sendRecorder: unexpected Bot API method")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, string(body))
	return r.reply, nil
}

func (r *sendRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sends)
}

// seedPairedThread pairs tierTestChat to the test bot's subject and records
// tier as threadID's privacy, through the real stores the daemon reads.
func seedPairedThread(t *testing.T, dataDir string, tier provider.SensitivityTier) {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	dbPath := filepath.Join(dataDir, "cascade.db")
	db := openTestConversationDB(t, dataDir)
	backups := filepath.Join(dataDir, "backups")
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("bridge schema: %v", err)
	}
	row := bridge.SubjectRow{Subject: telegram.SubjectFromToken(syntheticBotToken),
		TrustTier: "paired-device", PairedAt: clock.Now(), AllowedFrom: []string{tierTestChat}}
	if err := bridge.NewStore(db).Save(ctx, row); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("conversation schema: %v", err)
	}
	if err := conversation.NewStore(db).SetThreadPrivacy(ctx, tierTestThread, tier); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}
	seedBridgeThread(t, dataDir, 555000222, tierTestThread)
}

// armTierBridge assembles a real enabled bridge over a recording transport
// and arms bridge.send's sender with it.
func armTierBridge(t *testing.T, tier provider.SensitivityTier) (*sendRecorder, *recordingBus) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("windows tier-2 refuses at Send's platform gate before the tier decision")
	}
	resetChatSender(t)
	deps, bus, dataDir := enabledBridgeDeps(t)
	seedPairedThread(t, dataDir, tier)
	reply, err := os.ReadFile(filepath.Join("..", "..", "plugins", "cascade-pa", "telegram", "testdata", "sendmessage_ok.json"))
	if err != nil {
		t.Fatalf("read sendMessage fixture: %v", err)
	}
	rec := &sendRecorder{reply: reply}
	deps.Transport = rec
	rt, err := NewCascadePABridge(context.Background(), deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	closeBridgeRuntime(t, rt)
	WireChatBridge(rt, runtime.NewSystemClock())
	return rec, bus
}

func TestBridgeSendThreadTierGatesCallerSensitivity(t *testing.T) {
	t.Run("restricted thread refuses a public caller", func(t *testing.T) {
		rec, bus := armTierBridge(t, provider.SensitivityRestricted)
		res, err := BridgeSend(context.Background(), BridgeSendParams{
			ThreadID: tierTestThread, Content: "stay home", Sensitivity: tools.SensitivityPublic,
		})
		if err == nil || !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("BridgeSend = %+v, %v; want a KindPolicyDenied refusal from the thread tier", res, err)
		}
		if n := rec.count(); res.Mirrored || n != 0 {
			t.Fatalf("mirrored=%v with %d transport call(s); a refused send must leave the transport untouched", res.Mirrored, n)
		}
		got := bus.events()
		if len(got) != 1 || got[0].namespace != "bridge" || string(got[0].kind) != "bridge.refused" {
			t.Fatalf("bridge events = %+v, want exactly one bridge.refused", got)
		}
		if strings.Contains(got[0].payload, "stay home") {
			t.Fatal("bridge.refused carries the turn's content")
		}
	})
	t.Run("internal thread delivers one send", func(t *testing.T) {
		rec, bus := armTierBridge(t, provider.SensitivityInternal)
		res, err := BridgeSend(context.Background(), BridgeSendParams{
			ThreadID: tierTestThread, Content: "hello owner", Sensitivity: tools.SensitivityPublic,
		})
		if err != nil || !res.Mirrored {
			t.Fatalf("BridgeSend = %+v, %v; want mirrored", res, err)
		}
		if n := rec.count(); n != 1 {
			t.Fatalf("%d transport call(s), want exactly 1", n)
		}
		if n := len(bus.events()); n != 0 {
			t.Fatalf("a delivered send emitted %d bridge event(s), want 0", n)
		}
	})
}
