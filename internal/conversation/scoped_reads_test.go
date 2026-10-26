package conversation_test

// Purpose: acceptance for caller-principal scoping of the five chat read
//   verbs over the composed registry: anonymous refusal, a principal that
//   sees only its own project's threads, and an owner view unchanged.
// Inputs: a real SQLite conversation store, the real adapter and topic
//   handlers on an rpc.Registry with the access policy on, and principals
//   minted by internal/principal.
// Outputs: test verdicts only.
// Constraints: every positive thread and turn is written through
//   chat.append_turn with a minted principal; the only direct store write
//   is seedLegacy, a thread that predates attribution (TestScopedFixtureWritesOnlyViaVerbs).
// SPORT: internal.conversation.adapter (scoped reads).

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

type chatFixture struct {
	reg    *rpc.Registry
	store  conversation.Store
	minter *principal.Minter
	clock  *testkit.FrozenClock
}

// The five read verbs this ticket scopes, with params valid for each.
var scopedReadVerbs = []struct {
	method string
	params any
}{
	{"chat.list_threads", map[string]any{}},
	{"chat.get_thread", map[string]any{"thread_id": "legacy-thread"}},
	{"chat.search", map[string]any{"query": "beacon"}},
	{"chat.topics_list", nil},
	{"chat.threads_list", map[string]any{}},
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := conversation.ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	f := &chatFixture{reg: rpc.NewRegistry(), store: conversation.NewStore(db), minter: principal.NewMinter(clock), clock: clock}
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, clock.Now()) })
	conversation.NewAdapter(f.store, nil, nil, clock, conversation.ModeEmbedded).RegisterHandlers(f.reg)
	th, err := topics.NewRPCHandlers(f.store, clock)
	if err != nil {
		t.Fatal(err)
	}
	th.RegisterHandlers(f.reg)
	return f
}

// as mints a session principal for scope and returns its caller context.
func (f *chatFixture) as(t *testing.T, scope string) context.Context {
	t.Helper()
	token, err := f.minter.Mint(principal.Principal{ID: "p-" + scope, Kind: principal.KindSession, SessionID: "s-" + scope, Scope: scope}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.minter.Resolve(token, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func ownerCtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func (f *chatFixture) call(ctx context.Context, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	var raw json.RawMessage
	if params != nil {
		raw, _ = json.Marshal(params)
	}
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		return nil, eo
	}
	out, _ := json.Marshal(res)
	return out, nil
}

// appendAs writes one text turn through chat.append_turn; an empty
// threadID creates a thread. It returns the thread id the daemon used.
func (f *chatFixture) appendAs(ctx context.Context, t *testing.T, threadID, text string) string {
	t.Helper()
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": text}}}
	if threadID != "" {
		if _, ok, _ := f.store.GetThread(ctx, threadID); !ok {
			f.seedOwnerThread(t, threadID) // chat.append_turn no longer creates a caller-named thread
		}
		params["thread_id"] = threadID
	}
	out, eo := f.call(ctx, "chat.append_turn", params)
	if eo != nil {
		t.Fatalf("chat.append_turn(%q): %+v", threadID, eo)
	}
	var res struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.ThreadID == "" {
		t.Fatalf("chat.append_turn result %s: %v", out, err)
	}
	return res.ThreadID
}

// seedLegacy writes a thread directly, the way every thread created
// before thread_scope existed was written: it has no attribution row.
func (f *chatFixture) seedLegacy(t *testing.T, threadID, text string) {
	t.Helper()
	ctx := context.Background()
	turnID := conversation.NewTurnID(threadID, 0, conversation.RoleUser)
	if err := f.store.AppendTurn(ctx, conversation.Turn{ID: turnID, ThreadID: threadID, Role: conversation.RoleUser, CreatedAt: f.clock.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	seg := conversation.Segment{ID: conversation.NewSegmentID(turnID, 0, conversation.SegmentText), TurnID: turnID,
		Kind: conversation.SegmentText, Content: text, CreatedAt: f.clock.Now().Unix()}
	if err := f.store.AppendSegment(ctx, seg); err != nil {
		t.Fatal(err)
	}
}

// denied reports a KindPermissionDenied refusal, from the access policy or a handler.
func denied(eo *rpc.ErrorObject) bool {
	return eo != nil && eo.Code == cascade.KindPermissionDenied.JSONRPCCode()
}

func TestChatScopedReadsRefuseAnonymous(t *testing.T) {
	f := newChatFixture(t)
	f.seedLegacy(t, "legacy-thread", "beacon legacy")
	f.appendAs(ownerCtx(), t, "topic:general", "beacon owner")
	for _, v := range scopedReadVerbs {
		out, eo := f.call(context.Background(), v.method, v.params)
		if eo == nil || !denied(eo) || out != nil {
			t.Errorf("%s without a Caller: out=%s err=%+v, want KindPermissionDenied and no result", v.method, out, eo)
		}
	}
}

// TestScopedFixtureWritesOnlyViaVerbs keeps every positive case honest:
// a direct store write anywhere but seedLegacy fails the test.
func TestScopedFixtureWritesOnlyViaVerbs(t *testing.T) {
	src, err := os.ReadFile("scoped_reads_test.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`\.(Append` + `Turn|Append` + `Segment|Create` + `Thread|Pu` + `t)\(`)
	for _, fn := range strings.Split(string(src), "\nfunc ") {
		if direct.MatchString(fn) && !strings.HasPrefix(fn, "(f *chatFixture) seedLegacy(") {
			t.Errorf("direct store write outside seedLegacy in func %.60q", fn)
		}
	}
}

// ownerFixture writes the fixture store TestChatOwnerCLIUnchanged reads:
// one legacy thread, two owner threads named by the caller, three turns.
func ownerFixture(t *testing.T) *chatFixture {
	f := newChatFixture(t)
	f.seedLegacy(t, "legacy-thread", "beacon from before attribution")
	f.appendAs(ownerCtx(), t, "t-alpha", "alpha beacon first")
	f.appendAs(ownerCtx(), t, "t-alpha", "alpha second")
	f.appendAs(ownerCtx(), t, "topic:general", "general beacon")
	return f
}

// ownerViewWant is each verb's owner-view result on ownerFixture as the
// pre-scoping handlers return it (captured at the parent commit).
var ownerViewWant = map[string]string{
	"chat.list_threads":       `{"threads":[{"ID":"legacy-thread","Name":"legacy-thread","CreatedAt":1772600767},{"ID":"t-alpha","Name":"t-alpha","CreatedAt":1772600767},{"ID":"topic:general","Name":"topic:general","CreatedAt":1772600767}]}`,
	"chat.get_thread":         `{"thread":{"ID":"legacy-thread","Name":"legacy-thread","CreatedAt":1772600767},"turns":[{"turn":{"ID":"eab0efd97645a7ef8e3f15f7ae983433ff034a46ecd47966e7c9350433a9bea9","ThreadID":"legacy-thread","Seq":0,"Role":"user","CreatedAt":1772600767},"segments":[{"ID":"c6628c185a319d6036ca2d9e38a6f71c58c2526ead658c62ddc7908a11f6231c","TurnID":"eab0efd97645a7ef8e3f15f7ae983433ff034a46ecd47966e7c9350433a9bea9","Seq":0,"Kind":"text","Content":"beacon from before attribution","CreatedAt":1772600767}]}]}`,
	"chat.search":             `{"results":[{"thread_id":"topic:general","turn_id":"299224956fde8a99d6f98528775dbbcbd6f1a9cba2d798834342b9eb216cc71a","content":"general beacon","score":0.0000011255813953488372},{"thread_id":"t-alpha","turn_id":"99850dd85ded10e7a4717504752be7ebeb0d8b8e5ac62efcf5b4890989181798","content":"alpha beacon first","score":9.641434262948206e-7},{"thread_id":"legacy-thread","turn_id":"eab0efd97645a7ef8e3f15f7ae983433ff034a46ecd47966e7c9350433a9bea9","content":"beacon from before attribution","score":8.432055749128921e-7}]}`,
	"chat.topics_list":        `{"topics":[{"topic_type":"general","thread_count":1,"turn_count":1}]}`,
	"chat.threads_list":       `{"threads":[{"topic_type":"general","id":"topic:general","turn_count":1,"last_activity":1772600767}],"page":1,"page_size":20,"total_count":1}`,
	"chat.get_thread#t-alpha": `{"thread":{"ID":"t-alpha","Name":"t-alpha","CreatedAt":1772600767},"turns":[{"turn":{"ID":"99850dd85ded10e7a4717504752be7ebeb0d8b8e5ac62efcf5b4890989181798","ThreadID":"t-alpha","Seq":0,"Role":"user","CreatedAt":1772600767},"segments":[{"ID":"f59bf12fde856f4d4b4b4d972e5d20b8d238eb1cab3525d621989b7a5e5fb857","TurnID":"99850dd85ded10e7a4717504752be7ebeb0d8b8e5ac62efcf5b4890989181798","Seq":0,"Kind":"text","Content":"alpha beacon first","CreatedAt":1772600767}]},{"turn":{"ID":"932f392c0a3c7c70325703789cfdd20b484253d1df246eecaf336bad927ea76b","ThreadID":"t-alpha","Seq":1,"Role":"user","CreatedAt":1772600767},"segments":[{"ID":"fb31bc2e71443f6ebb3688d909c574a1e2f57aceebc858322346feeecb727c59","TurnID":"932f392c0a3c7c70325703789cfdd20b484253d1df246eecaf336bad927ea76b","Seq":0,"Kind":"text","Content":"alpha second","CreatedAt":1772600767}]}]}`,
}

func TestChatOwnerCLIUnchanged(t *testing.T) {
	f := ownerFixture(t)
	verbs := append(scopedReadVerbs, struct {
		method string
		params any
	}{"chat.get_thread", map[string]any{"thread_id": "t-alpha"}})
	for i, v := range verbs {
		out, eo := f.call(ownerCtx(), v.method, v.params)
		if eo != nil {
			t.Fatalf("%s: %+v", v.method, eo)
		}
		key := v.method
		if i == len(verbs)-1 {
			key += "#t-alpha"
		}
		if os.Getenv("SCOPED_READS_CAPTURE") != "" {
			t.Logf("CAPTURE %q: `%s`,", key, out)
			continue
		}
		if string(out) != ownerViewWant[key] {
			t.Errorf("%s owner view changed:\n got %s\nwant %s", key, out, ownerViewWant[key])
		}
	}
}

// threadIDs collects every thread id a read verb's result names.
func threadIDs(t *testing.T, raw json.RawMessage) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(?:ID|id|thread_id)":"([^"]+)"`).FindAllStringSubmatch(string(raw), -1) {
		ids[m[1]] = true
	}
	return ids
}

func TestChatPrincipalSeesOnlyOwnScope(t *testing.T) {
	f := newChatFixture(t)
	a, b := f.as(t, "project:a"), f.as(t, "project:b")
	f.seedLegacy(t, "legacy-thread", "beacon legacy")
	f.appendAs(ownerCtx(), t, "topic:general", "beacon owner topic")
	aThread := f.appendAs(a, t, "", "beacon alpha")
	if f.appendAs(a, t, aThread, "beacon alpha again") != aThread {
		t.Fatal("continuing an own thread moved threads")
	}
	bThread := f.appendAs(b, t, "", "beacon bravo")
	foreign := []string{bThread, "legacy-thread", "topic:general"}
	reads := []struct {
		method string
		params map[string]any
	}{{"chat.list_threads", map[string]any{}}, {"chat.get_thread", map[string]any{"thread_id": aThread}},
		{"chat.search", map[string]any{"query": "beacon"}}, {"chat.topics_list", map[string]any{}},
		{"chat.threads_list", map[string]any{}}}
	for _, r := range reads {
		out, eo := f.call(a, r.method, r.params)
		if eo != nil {
			t.Fatalf("%s as project:a: %+v", r.method, eo)
		}
		ids := threadIDs(t, out)
		for _, id := range foreign {
			if ids[id] || strings.Contains(string(out), "bravo") || strings.Contains(string(out), "legacy") {
				t.Errorf("%s leaked %s to project:a: %s", r.method, id, out)
			}
		}
		if wantOwn := r.method == "chat.list_threads" || r.method == "chat.get_thread" || r.method == "chat.search"; wantOwn != ids[aThread] {
			t.Errorf("%s as project:a: own thread listed=%t: %s", r.method, ids[aThread], out)
		}
		r.params["scope"] = "project:b"
		if out, eo := f.call(a, r.method, r.params); !denied(eo) || out != nil {
			t.Errorf("%s declaring project:b as project:a: %s %+v", r.method, out, eo)
		}
		r.params["scope"] = "project:a"
		if _, eo := f.call(a, r.method, r.params); eo != nil {
			t.Errorf("%s declaring its own scope: %+v", r.method, eo)
		}
	}
	if out, _ := f.call(a, "chat.search", map[string]any{"query": "beacon"}); strings.Count(string(out), aThread) != 2 {
		t.Errorf("search as project:a did not return both own hits: %s", out)
	}
	f.assertForeignRefused(a, t, foreign)
	if out, _ := f.call(b, "chat.list_threads", map[string]any{}); !threadIDs(t, out)[bThread] || threadIDs(t, out)[aThread] {
		t.Errorf("project:b list: %s", out)
	}
	if out, _ := f.call(ownerCtx(), "chat.list_threads", nil); len(threadIDs(t, out)) != 4 {
		t.Errorf("owner list lost threads: %s", out)
	}
}

// assertForeignRefused: ctx is refused every foreign id via get_thread and search.
func (f *chatFixture) assertForeignRefused(ctx context.Context, t *testing.T, foreign []string) {
	t.Helper()
	for _, id := range foreign {
		if out, eo := f.call(ctx, "chat.get_thread", map[string]any{"thread_id": id}); !denied(eo) {
			t.Errorf("get_thread %s: %s %+v", id, out, eo)
		}
		if out, eo := f.call(ctx, "chat.search", map[string]any{"query": "beacon", "thread_id": id}); !denied(eo) {
			t.Errorf("search in %s: %s %+v", id, out, eo)
		}
	}
}
