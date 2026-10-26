// Purpose: the guards on the chat turn journal's composition (wireChatJournal
// and its call in wireChatHandlers). Each test goes through wireChatHandlers
// and a registry it built, never through conversation.Adapter directly:
// internal/conversation already proves the adapter journals once SetJournal
// is called, and the defect these tests exist for is that nothing called it.
//
// Inputs: an in-memory provider.Store as the runtime store, a throwaway
// cascade home, and a scratch conversation store used only to produce
// journal entries in the real on-disk format (no hand-built payloads).
// Outputs: none. Constraints: HOME and CASCADE_HOME stay under the shared
// test HOME; the custody is the injected file vault (testChatCustody).
//
// SPORT: cmd/cascade chat journal wiring tests (ADD) — P1-CPA-08.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scratchConversationStore opens a conversation store on its own throwaway
// sqlite file. It is the "other machine" a journaled turn was first
// committed on: seeding through it leaves the journal entry in the runtime
// store while the daemon's own cascade.db has no such rows.
func scratchConversationStore(t *testing.T, clock cascaderuntime.Clock) conversation.Store {
	t.Helper()
	dir := shortCascadeHome(t)
	dbPath := filepath.Join(dir, "scratch.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open scratch conversation db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := conversation.ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock,
		dbPath, filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("apply conversation schema to the scratch db: %v", err)
	}
	return conversation.NewStore(db)
}

// seedAckedChatTurn writes one ACKed TurnAppend journal entry for threadID
// into store's chat journal namespace, through the production write path
// (conversation.AppendTurnJournaled), and returns the turn's id.
func seedAckedChatTurn(t *testing.T, store provider.Store, clock cascaderuntime.Clock, threadID, text string) string {
	t.Helper()
	now := clock.Now().Unix()
	turn := conversation.Turn{
		ID: conversation.NewTurnID(threadID, 0, conversation.RoleUser), ThreadID: threadID,
		Seq: 0, Role: conversation.RoleUser, CreatedAt: now,
	}
	seg := conversation.Segment{
		ID: conversation.NewSegmentID(turn.ID, 0, conversation.SegmentText), TurnID: turn.ID,
		Seq: 0, Kind: conversation.SegmentText, Content: text, CreatedAt: now,
	}
	js := journal.New(store, clock, chatJournalNamespace)
	if err := conversation.AppendTurnJournaled(context.Background(), js, scratchConversationStore(t, clock), turn,
		[]conversation.Segment{seg}); err != nil {
		t.Fatalf("seed an ACKed TurnAppend entry: %v", err)
	}
	return turn.ID
}

// seedUndecodableChatEntry writes an ACKed entry whose payload is valid JSON
// of the wrong shape, so conversation.Resume fails on it with
// ErrJournalDecodeFailed.
func seedUndecodableChatEntry(t *testing.T, store provider.Store, clock cascaderuntime.Clock, threadID string) {
	t.Helper()
	js := journal.New(store, clock, chatJournalNamespace)
	if _, err := js.Append(context.Background(), threadID, journal.KindAck, "op-bad", json.RawMessage(`[1,2]`)); err != nil {
		t.Fatalf("seed an undecodable journal entry: %v", err)
	}
}

// wireChatForTest runs wireChatHandlers over a throwaway cascade home and
// the given runtime store.
func wireChatForTest(t *testing.T, store provider.Store, clock cascaderuntime.Clock) (*rpc.Registry, error) {
	t.Helper()
	registry := rpc.NewRegistry()
	bus := events.New(storetest.NewMemStore(), clock)
	err := wireChatHandlers(context.Background(), registry, fakeDaemonPaths{root: shortCascadeHome(t)}, clock, bus,
		testChatCustody(t, nil), store)
	return registry, err
}

// chatDispatch runs one chat.* request through registry and returns the
// result re-decoded as a generic map.
func chatDispatch(t *testing.T, registry *rpc.Registry, method string, params map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	req, parseErr := rpc.Parse(body)
	if parseErr != nil {
		t.Fatalf("parse %s: %+v", method, parseErr)
	}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("%s: %+v", method, errObj)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("re-marshal %s result: %v", method, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s result: %v", method, err)
	}
	return out
}

func chatMethods(registry *rpc.Registry) []string {
	var out []string
	for _, m := range registry.Methods() {
		if strings.HasPrefix(m, "chat.") {
			out = append(out, m)
		}
	}
	return out
}

// TestChatWiring_JournalWiredAtCompositionRoot proves wireChatHandlers sets
// the journal: a chat.append_turn through the registry it built leaves an
// ACKed entry in the "conversation.journal" namespace of the runtime store.
// With SetJournal skipped the namespace stays empty and this fails.
func TestChatWiring_JournalWiredAtCompositionRoot(t *testing.T) {
	clock := cascaderuntime.NewSystemClock()
	store := storetest.NewMemStore()
	registry, err := wireChatForTest(t, store, clock)
	if err != nil {
		t.Fatalf("wireChatHandlers: %v", err)
	}

	// No thread_id: the daemon mints the thread this first turn creates.
	res := chatDispatch(t, registry, "chat.append_turn", map[string]any{
		"role":     "user",
		"segments": []map[string]string{{"kind": "text", "content": "journal me"}},
	})
	threadID, _ := res["thread_id"].(string)
	if threadID == "" {
		t.Fatalf("chat.append_turn returned no thread id: %v", res)
	}

	js := journal.New(store, clock, chatJournalNamespace)
	entries, err := js.Replay(context.Background(), threadID, journal.Cursor{}, []journal.Kind{journal.KindAck})
	if err != nil {
		t.Fatalf("replay the chat journal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("chat journal holds %d ACKed entries after one chat.append_turn, want 1", len(entries))
	}
	other := journal.New(store, clock, journal.DefaultNamespace)
	leaked, err := other.ListEntities(context.Background())
	if err != nil {
		t.Fatalf("list the default journal namespace: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("chat entries leaked into the default journal namespace: %v", leaked)
	}
}

// scanSpyStore reports, on the first Scan of the chat journal namespace, the
// chat.* methods already registered. ListEntities is that first Scan, so the
// recorded count is "what clients could already call when resume began".
type scanSpyStore struct {
	provider.Store
	mu       sync.Mutex
	registry *rpc.Registry
	seen     bool
	atResume []string
}

func (s *scanSpyStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if namespace == chatJournalNamespace {
		s.mu.Lock()
		if !s.seen && s.registry != nil {
			s.seen = true
			s.atResume = chatMethods(s.registry)
		}
		s.mu.Unlock()
	}
	return s.Store.Scan(ctx, namespace, prefix)
}

// TestChatWiring_ResumesJournaledTurnsBeforeRegister seeds an ACKed turn the
// conversation store does not hold, wires chat, and requires both that
// chat.get_thread returns the turn and that no chat.* method existed when
// the resume scan started.
func TestChatWiring_ResumesJournaledTurnsBeforeRegister(t *testing.T) {
	clock := cascaderuntime.NewSystemClock()
	spy := &scanSpyStore{Store: storetest.NewMemStore()}
	turnID := seedAckedChatTurn(t, spy, clock, "th-resume", "survives")

	registry := rpc.NewRegistry()
	spy.registry = registry
	bus := events.New(storetest.NewMemStore(), clock)
	if err := wireChatHandlers(context.Background(), registry, fakeDaemonPaths{root: shortCascadeHome(t)}, clock, bus,
		testChatCustody(t, nil), spy); err != nil {
		t.Fatalf("wireChatHandlers: %v", err)
	}

	if !spy.seen {
		t.Fatal("the resume scan never touched the chat journal namespace")
	}
	if len(spy.atResume) != 0 {
		t.Fatalf("chat.* methods were registered before resume began: %v", spy.atResume)
	}
	thread := chatDispatch(t, registry, "chat.get_thread", map[string]any{"thread_id": "th-resume"})
	turns, _ := thread["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("chat.get_thread returned %d turns after resume, want 1: %v", len(turns), thread)
	}
	if got := turns[0].(map[string]any)["turn"].(map[string]any)["ID"]; got != turnID {
		t.Fatalf("resumed turn id = %v, want %s", got, turnID)
	}
}

// TestChatWiring_ResumeErrorRefusesRegistration seeds an entry Resume cannot
// decode. wireChatHandlers must return KindIntegrity naming the thread, with
// the decode failure as the cause (by identity), and register no chat.*
// method.
func TestChatWiring_ResumeErrorRefusesRegistration(t *testing.T) {
	clock := cascaderuntime.NewSystemClock()
	store := storetest.NewMemStore()
	seedUndecodableChatEntry(t, store, clock, "th-bad")

	registry, err := wireChatForTest(t, store, clock)
	if err == nil {
		t.Fatal("wireChatHandlers succeeded over a journal that cannot resume")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("resume failure kind = %v (ok=%v), want KindIntegrity: %v", kind, ok, err)
	}
	const want = "chat: resume journaled thread th-bad"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the thread (want %q)", err.Error(), want)
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Err != conversation.ErrJournalDecodeFailed {
		t.Fatalf("cause is not conversation.ErrJournalDecodeFailed by identity: %#v", ce)
	}
	if got := chatMethods(registry); len(got) != 0 {
		t.Fatalf("chat.* methods registered despite a failed resume: %v", got)
	}
}

// TestChatWiring_NilStoreWarnsOnce covers the store-less composition: chat
// still registers and works, nothing is journaled, and exactly one WARN with
// the documented text is logged.
func TestChatWiring_NilStoreWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	registry, err := wireChatForTest(t, nil, cascaderuntime.NewSystemClock())
	if err != nil {
		t.Fatalf("wireChatHandlers with a nil store: %v", err)
	}
	chatDispatch(t, registry, "chat.append_turn", map[string]any{
		"role":     "user",
		"segments": []map[string]string{{"kind": "text", "content": "unjournaled"}},
	})

	var warns int
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "chat: no runtime store; turns are not journaled" {
			warns++
			if rec["level"] != "WARN" {
				t.Fatalf("no-store line logged at %v, want WARN", rec["level"])
			}
		}
	}
	if warns != 1 {
		t.Fatalf("no-store WARN logged %d times, want exactly 1:\n%s", warns, buf.String())
	}
}
