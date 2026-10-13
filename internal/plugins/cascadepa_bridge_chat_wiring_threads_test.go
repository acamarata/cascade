package plugins

// Purpose (this file): mapRig, the real-composition fixture the
//   chat-to-thread map acceptance tests (cascadepa_bridge_threadmap_test.go)
//   run on: a REAL enabled bridge from NewCascadePABridge (enabledBridge ->
//   NewCascadePABridgeChatHandler -> the durable map), its REAL poll loop
//   and dispatch gates fed Bot API updates by a recording transport, and the
//   REAL chat.* adapter (chat.create_thread, chat.append_turn) on an
//   access-checked registry over the same cascade.db, reached through
//   bridgeChatService's rpcDoer seam with the owner-CLI caller the daemon
//   socket would stamp.
// Constraints: synthetic token and chat ids, file-only vault, no network,
//   HOME/USERPROFILE/CASCADE_HOME under t.TempDir(). Windows tier-2 refuses
//   the poll loop, so users of this rig skip there.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (TEST: thread map rig).

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// mapOwner is the synthetic paired owner; its DM chat id equals its user id.
const mapOwner = int64(555000333)

// registryDoer is bridgeChatService's rpcDoer over a real *rpc.Registry. It
// records every call; gate, when set, runs before each dispatch.
type registryDoer struct {
	reg    *rpc.Registry
	mu     sync.Mutex
	calls  []doerCall
	minted []string
	gate   func(method string)

	appendMu sync.Mutex // serializes chat.append_turn dispatch (see Do)
}

type doerCall struct {
	method string
	params map[string]any
}

func (d *registryDoer) Do(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	d.mu.Lock()
	d.calls = append(d.calls, doerCall{method: method, params: fields})
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		gate(method)
	}
	if method == conversation.MethodAppendTurn {
		// chat.append_turn derives the turn id from the thread's turn count,
		// so two concurrent appends to one thread collide; the rig serializes
		// them (recorded as a daemon backlog finding, not this test's subject).
		d.appendMu.Lock()
		defer d.appendMu.Unlock()
	}
	owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
	res, eo := d.reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		return eo
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if method == conversation.MethodCreateThread {
		var minted struct {
			ThreadID string `json:"thread_id"`
		}
		_ = json.Unmarshal(encoded, &minted)
		d.mu.Lock()
		d.minted = append(d.minted, minted.ThreadID)
		d.mu.Unlock()
	}
	return json.Unmarshal(encoded, out)
}

// mintedIDs returns every thread id chat.create_thread returned.
func (d *registryDoer) mintedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.minted...)
}

// threadsOf returns the thread_id of every call to method, in order.
func (d *registryDoer) threadsOf(method string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if c.method == method {
			id, _ := c.params["thread_id"].(string)
			out = append(out, id)
		}
	}
	return out
}

// mapRig is one data directory with a paired owner, the daemon's chat.*
// adapter and the doer every bridge built on it dials.
type mapRig struct {
	deps   BridgeDeps
	db     *sql.DB
	store  conversation.Store
	daemon *registryDoer
}

func newMapRig(t *testing.T) *mapRig {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("windows tier-2 refuses the bridge's poll loop")
	}
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	deps, _, dataDir := enabledBridgeDeps(t)
	ctx, dbPath, backups := context.Background(), filepath.Join(dataDir, "cascade.db"), filepath.Join(dataDir, "backups")
	db := openTestConversationDB(t, dataDir)
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, deps.Clock, dbPath, backups); err != nil {
		t.Fatalf("bridge schema: %v", err)
	}
	row := bridge.SubjectRow{Subject: telegram.SubjectFromToken(syntheticBotToken), TrustTier: "paired-device",
		PairedAt: deps.Clock.Now(), AllowedFrom: []string{jsonInt(mapOwner)}}
	if err := bridge.NewStore(db).Save(ctx, row); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, deps.Clock, dbPath, backups); err != nil {
		t.Fatalf("conversation schema: %v", err)
	}
	r := &mapRig{deps: deps, db: db, store: conversation.NewStore(db)}
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "no principal tokens in this test")
	})
	conversation.NewAdapter(r.store, nil, nil, deps.Clock, conversation.ModeEmbedded).RegisterHandlers(reg)
	r.daemon = &registryDoer{reg: reg}
	prev := bridgeChatFactory
	bridgeChatFactory = func() bridgeChatService { return bridgeChatService{doer: r.daemon} }
	t.Cleanup(func() { bridgeChatFactory = prev })
	return r
}

func jsonInt(n int64) string { return strconv.FormatInt(n, 10) }

// textUpdateJSON is one Bot API text update from mapOwner in chat (type kind).
func textUpdateJSON(id, chat int64, kind, text string) string {
	return `{"update_id":` + jsonInt(id) + `,"message":{"message_id":` + jsonInt(id) + `,"from":{"id":` +
		jsonInt(mapOwner) + `,"is_bot":false,"first_name":"T"},"chat":{"id":` + jsonInt(chat) + `,"type":"` + kind +
		`"},"date":1758300000,"text":` + strconv.Quote(text) + `}}`
}

// updatePoster serves one getUpdates batch, then reports each later poll on
// polled (the batch has been dispatched) and parks until the poll ends.
type updatePoster struct {
	batch  string
	once   sync.Once
	polled chan struct{}
	mu     sync.Mutex
	sends  []string
}

func (p *updatePoster) Post(ctx context.Context, url string, body []byte) ([]byte, error) {
	if strings.HasSuffix(url, "/"+telegram.MethodSendMessage) {
		p.mu.Lock()
		p.sends = append(p.sends, string(body))
		p.mu.Unlock()
		return []byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`), nil
	}
	served := false
	p.once.Do(func() { served = true })
	if served {
		return []byte(`{"ok":true,"result":[` + p.batch + `]}`), nil
	}
	select {
	case p.polled <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *updatePoster) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sends...)
}

// deliver builds a fresh enabled bridge on the rig's data directory (a
// restart each call), feeds it updates through the real poll loop, waits
// until they are dispatched and stops it. It returns the runtime and its
// transport for assertions.
func (r *mapRig) deliver(t *testing.T, updates ...string) (*BridgeRuntime, *updatePoster) {
	t.Helper()
	rt, poster := r.start(t, updates...)
	r.await(t, poster)
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("stop bridge: %v", err)
	}
	return rt, poster
}

func (r *mapRig) start(t *testing.T, updates ...string) (*BridgeRuntime, *updatePoster) {
	t.Helper()
	poster := &updatePoster{batch: strings.Join(updates, ","), polled: make(chan struct{}, 1)}
	deps := r.deps
	deps.Transport = poster
	rt, err := NewCascadePABridge(context.Background(), deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return rt, poster
}

func (r *mapRig) await(t *testing.T, poster *updatePoster) {
	t.Helper()
	select {
	case <-poster.polled:
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge never polled again; the batch was not dispatched")
	}
}

// IsBridgeThread gives the bridge.send tests' recording sender
// (cascadepa_bridge_chatsend_wiring_test.go) the map lookup bridge.send now
// asks every ChatBridge: its map holds exactly bridgedThreadID.
func (f *recordingChatSender) IsBridgeThread(_ context.Context, threadID string) (bool, error) {
	return threadID == bridgedThreadID, nil
}

// seedBridgeThread binds chat to threadID in dataDir's durable map, as the
// owner's first message would have, for an outbound test that starts from
// an already-mapped chat.
func seedBridgeThread(t *testing.T, dataDir string, chat int64, threadID string) {
	t.Helper()
	store, err := OpenBridgeThreadMap(context.Background(), openTestConversationDB(t, dataDir), dataDir, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("OpenBridgeThreadMap: %v", err)
	}
	if _, err := store.Bind(context.Background(), telegram.SubjectFromToken(syntheticBotToken), chat, threadID); err != nil {
		t.Fatalf("seed map row: %v", err)
	}
}

// TestBridgeMirroringConversations_UnmappedThread_CarriesNoOutcome: a
// minted-shape id may be a bridge thread, so the client asks the daemon;
// when the daemon's map does not hold it the Append carries no outcome.
func TestBridgeMirroringConversations_UnmappedThread_CarriesNoOutcome(t *testing.T) {
	doer := &recordingBridgeSendDoer{res: &BridgeSendResult{Reason: reasonNotBridgeThread}}
	inner := &stubConversations{res: tools.AppendResult{ThreadID: "thread-7", TurnID: "turn-1"}}
	conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: doer}}
	res, err := conv.Append(context.Background(), tools.AppendRequest{ThreadID: "thread-7", Role: "user", Content: "hi"})
	if err != nil || len(doer.calls) != 1 || res.Bridge != nil {
		t.Fatalf("Append = %+v, %v with %d bridge.send calls; want one call and no outcome", res, err, len(doer.calls))
	}
}
