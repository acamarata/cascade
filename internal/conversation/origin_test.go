package conversation

// Purpose: the untrusted-origin marker's tests (origin.go), driven through
//   the real chat.* registry entry points and a real SQLite store: the
//   append, get_thread, search and journal-resume paths all return the
//   marker, a bridge origin can never be stored trusted, and the marker row
//   commits with its turn.
// SPORT: internal.conversation.origin/ADDED (tests) (contract:chat-turn-origin).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// noStore is a Store whose every method panics (nil embedded interface): an
// adapter built on it proves a request was refused before any store access.
type noStore struct{ Store }

// refusedBeforeStore fails t, rather than crashing the binary, when a request
// reached a noStore.
func refusedBeforeStore(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("the store was reached before the origin refusal: %v", r)
	}
}

func appendMarked(t *testing.T, reg *rpc.Registry, thread, text, origin string, untrusted bool) *rpc.ErrorObject {
	t.Helper()
	_, errObj := dispatch(t, reg, MethodAppendTurn, appendTurnParams{ThreadID: thread, Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: text}}, Origin: origin, Untrusted: untrusted})
	return errObj
}

func TestAppendTurn_PersistsUntrustedOriginMarker(t *testing.T) {
	adapter, reg, _ := newTestAdapter(t)
	seedNamedThreads(t, adapter.store, "th-o")
	if e := appendMarked(t, reg, "th-o", "from the bridge", OriginBridgeTelegram, true); e != nil {
		t.Fatalf("marked append errored: %+v", e)
	}
	turns, err := adapter.store.ListTurns(context.Background(), "th-o")
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurns = %v, %v; want one turn", turns, err)
	}
	if turns[0].Origin != OriginBridgeTelegram || turns[0].Provenance != provider.ProvenanceUntrustedSource {
		t.Fatalf("stored marker = %q/%q, want bridge-telegram/untrusted-source", turns[0].Origin, turns[0].Provenance)
	}
	res, errObj := dispatch(t, reg, MethodGetThread, getThreadParams{ThreadID: "th-o"})
	if errObj != nil {
		t.Fatalf("chat.get_thread errored: %+v", errObj)
	}
	wire, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal get_thread: %v", err)
	}
	for _, want := range []string{`"origin":"bridge-telegram"`, `"untrusted":true`} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("chat.get_thread wire %s lacks %s", wire, want)
		}
	}
	// A local untrusted append (no origin) is accepted and stored untrusted.
	seedNamedThreads(t, adapter.store, "th-u")
	if e := appendMarked(t, reg, "th-u", "pasted", OriginLocal, true); e != nil {
		t.Fatalf("local untrusted append errored: %+v", e)
	}
	got, _ := adapter.store.ListTurns(context.Background(), "th-u")
	if len(got) != 1 || got[0].Origin != OriginLocal || got[0].Provenance != provider.ProvenanceUntrustedSource {
		t.Fatalf("local untrusted turn read back as %+v", got)
	}
}

func TestAppendTurn_RefusesTrustedBridgeOrigin(t *testing.T) {
	defer refusedBeforeStore(t)
	reg := rpc.NewRegistry()
	NewAdapter(noStore{}, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "").RegisterHandlers(reg)
	for _, origin := range []string{OriginBridgeTelegram, OriginBridgeWhatsApp} {
		e := appendMarked(t, reg, "th-x", "claims to be trusted", origin, false)
		if e == nil || e.Code != cascade.RPCCodeInvalidInput {
			t.Fatalf("origin %q untrusted=false: errObj = %+v, want RPCCodeInvalidInput before any store access", origin, e)
		}
	}
}

func TestAppendTurn_RefusesUnknownOrigin(t *testing.T) {
	defer refusedBeforeStore(t)
	reg := rpc.NewRegistry()
	NewAdapter(noStore{}, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "").RegisterHandlers(reg)
	for _, origin := range []string{"bridge-signal", "Bridge-Telegram", " bridge-telegram", "local", "trusted"} {
		e := appendMarked(t, reg, "th-x", "unknown origin", origin, true)
		if e == nil || e.Code != cascade.RPCCodeInvalidInput {
			t.Fatalf("origin %q: errObj = %+v, want RPCCodeInvalidInput before any store access", origin, e)
		}
		if strings.Contains(e.Message, strings.TrimSpace(origin)) {
			t.Errorf("refusal message %q echoes the offending origin", e.Message)
		}
	}
}

func TestOriginMarkerSurvivesJournalResume(t *testing.T) {
	store := newTestStore(t)
	adapter := NewAdapter(store, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "")
	js := newTestJournal(t)
	adapter.SetJournal(js)
	reg := rpc.NewRegistry()
	adapter.RegisterHandlers(reg)
	seedNamedThreads(t, store, "th-j")
	if e := appendMarked(t, reg, "th-j", "bridge turn", OriginBridgeWhatsApp, true); e != nil {
		t.Fatalf("bridge append: %+v", e)
	}
	if e := appendMarked(t, reg, "th-j", "plain local turn", OriginLocal, false); e != nil {
		t.Fatalf("local append: %+v", e)
	}

	fresh := newTestStore(t) // a store that lost everything: Resume rebuilds it
	if n, err := Resume(context.Background(), js, fresh, "th-j"); err != nil || n == 0 {
		t.Fatalf("Resume = %d, %v; want turns re-applied", n, err)
	}
	got, err := fresh.ListTurns(context.Background(), "th-j")
	if err != nil || len(got) != 2 {
		t.Fatalf("resumed ListTurns = %v, %v; want two turns", got, err)
	}
	if got[0].Origin != OriginBridgeWhatsApp || got[0].Provenance != provider.ProvenanceUntrustedSource {
		t.Errorf("resumed bridge turn = %q/%q, want bridge-whatsapp/untrusted-source", got[0].Origin, got[0].Provenance)
	}
	if got[1].Origin != OriginLocal || got[1].Provenance != provider.ProvenanceTrusted {
		t.Errorf("resumed local turn = %q/%q, want unmarked/trusted", got[1].Origin, got[1].Provenance)
	}
}

func TestSearchTurns_CarriesUntrustedOriginMarker(t *testing.T) {
	adapter, reg, _ := newTestAdapter(t)
	seedNamedThreads(t, adapter.store, "th-s1", "th-s2")
	if e := appendMarked(t, reg, "th-s1", "quokka from the bridge", OriginBridgeTelegram, true); e != nil {
		t.Fatalf("bridge append: %+v", e)
	}
	if e := appendMarked(t, reg, "th-s2", "quokka from the keyboard", OriginLocal, false); e != nil {
		t.Fatalf("local append: %+v", e)
	}
	res, errObj := dispatch(t, reg, MethodSearch, searchParams{Query: "quokka"})
	if errObj != nil {
		t.Fatalf("chat.search errored: %+v", errObj)
	}
	set, ok := res.(searchResultSet)
	if !ok || len(set.Results) != 2 {
		t.Fatalf("chat.search result = %#v, want two hits", res)
	}
	for _, r := range set.Results {
		bridge := r.ThreadID == "th-s1"
		if bridge != (r.Origin == OriginBridgeTelegram) || bridge != r.Untrusted {
			t.Errorf("hit %+v: bridge=%v but origin/untrusted disagree", r, bridge)
		}
	}
	wire, _ := json.Marshal(set)
	if strings.Count(string(wire), `"untrusted":true`) != 1 || strings.Count(string(wire), `"origin"`) != 1 {
		t.Errorf("search wire %s should mark exactly the bridge hit", wire)
	}
}

func TestAppendTurn_OriginInsertFailureRollsBackTurn(t *testing.T) {
	store, db := newTestStoreWithDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TABLE `+tableTurnOrigin); err != nil {
		t.Fatalf("drop origin table: %v", err)
	}
	turn := Turn{ID: NewTurnID("th-r", 0, RoleUser), ThreadID: "th-r", Role: RoleUser, CreatedAt: 1,
		Origin: OriginBridgeTelegram, Provenance: provider.ProvenanceUntrustedSource}
	if err := store.AppendTurn(ctx, turn); err == nil {
		t.Fatal("AppendTurn with the origin insert failing returned nil")
	}
	for _, table := range []string{tableTurn, tableThread} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows after a failed marked append = %d, %v; want 0 (rolled back)", table, n, err)
		}
	}
	// Control: an unmarked turn needs no origin row, so it still commits.
	if err := store.AppendTurn(ctx, Turn{ID: NewTurnID("th-r", 0, RoleUser), ThreadID: "th-r", Role: RoleUser, CreatedAt: 1}); err != nil {
		t.Fatalf("unmarked AppendTurn on the same db: %v", err)
	}
}

func TestAppendTurn_LocalAppendWritesNoOriginRow(t *testing.T) {
	store, db := newTestStoreWithDB(t)
	reg := rpc.NewRegistry()
	NewAdapter(store, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "").RegisterHandlers(reg)
	seedNamedThreads(t, store, "th-l")
	if e := appendMarked(t, reg, "th-l", "just me", OriginLocal, false); e != nil {
		t.Fatalf("local append: %+v", e)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tableTurnOrigin).Scan(&n); err != nil || n != 0 {
		t.Fatalf("origin rows after a local append = %d, %v; want 0", n, err)
	}
	got, err := store.ListTurns(context.Background(), "th-l")
	if err != nil || len(got) != 1 || got[0].Origin != OriginLocal || got[0].Provenance != provider.ProvenanceTrusted {
		t.Fatalf("local turn read back as %+v, %v; want Origin \"\" and trusted", got, err)
	}
}

func TestOriginRowCorruptReadsUntrusted(t *testing.T) {
	store, db := newTestStoreWithDB(t)
	ctx := context.Background()
	turn := Turn{ID: NewTurnID("th-c", 0, RoleUser), ThreadID: "th-c", Role: RoleUser, CreatedAt: 1,
		Origin: OriginBridgeTelegram, Provenance: provider.ProvenanceUntrustedSource}
	if err := store.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	for _, bad := range []string{"trusted ", "TRUSTED", "", "bogus", "trusted"} { // the last: a bridge row that parses but must never read trusted
		if _, err := db.ExecContext(ctx, `UPDATE `+tableTurnOrigin+` SET provenance = ?`, bad); err != nil {
			t.Fatalf("corrupt row: %v", err)
		}
		got, err := store.ListTurns(ctx, "th-c")
		if err != nil || len(got) != 1 || got[0].Provenance != provider.ProvenanceUntrustedSource {
			t.Fatalf("stored provenance %q read back as %+v, %v; want untrusted-source", bad, got, err)
		}
	}
}

func TestAppendTurn_StoreRefusesTrustedBridgeTurn(t *testing.T) {
	store := newTestStore(t)
	for _, prov := range []provider.Provenance{"", provider.ProvenanceTrusted, "bogus"} {
		turn := Turn{ID: NewTurnID("th-t", 0, RoleUser), ThreadID: "th-t", Role: RoleUser, CreatedAt: 1,
			Origin: OriginBridgeTelegram, Provenance: prov}
		if err := store.AppendTurn(context.Background(), turn); err != ErrInvalidRecord {
			t.Errorf("bridge turn with provenance %q: err = %v, want ErrInvalidRecord", prov, err)
		}
	}
	bad := Turn{ID: "x", ThreadID: "th-t", Role: RoleUser, CreatedAt: 1, Origin: "bridge-other", Provenance: provider.ProvenanceUntrustedSource}
	if err := store.AppendTurn(context.Background(), bad); err != ErrInvalidRecord {
		t.Errorf("unknown origin: err = %v, want ErrInvalidRecord", err)
	}
}

func TestRestoreMarker_NeverReplaysABridgeTurnAsTrusted(t *testing.T) {
	bridge := Turn{Origin: OriginBridgeTelegram}
	cases := []struct {
		name string
		in   Turn
		prov provider.Provenance
		want provider.Provenance
	}{
		{"recorded provenance wins", bridge, provider.ProvenanceUntrustedSource, provider.ProvenanceUntrustedSource},
		{"bridge entry that lost its provenance", bridge, "", provider.ProvenanceUntrustedSource},
		{"unmarked local entry stays unmarked", Turn{}, "", ""},
	}
	for _, c := range cases {
		if got := restoreMarker(c.in, c.prov).Provenance; got != c.want {
			t.Errorf("%s: Provenance = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOriginMarkerResume_CraftedAndRepeated(t *testing.T) {
	ctx, js := context.Background(), newTestJournal(t)
	bridge := Turn{ID: NewTurnID("th-z", 0, RoleUser), ThreadID: "th-z", Role: RoleUser, CreatedAt: 1, Origin: OriginBridgeTelegram}
	craft := func(prov provider.Provenance) {
		raw, _ := json.Marshal(journalPayload{Type: JournalEntryTurnAppend, Turn: &bridge, Provenance: prov})
		if _, err := js.Append(ctx, "th-z", journal.KindAck, bridge.ID, raw); err != nil {
			t.Fatalf("journal append: %v", err)
		}
	}
	craft(provider.ProvenanceTrusted) // a bridge entry claiming trusted must not be replayed
	store := newTestStore(t)
	if _, err := Resume(ctx, js, store, "th-z"); err != ErrInvalidRecord {
		t.Fatalf("Resume of a bridge/trusted entry: err = %v, want ErrInvalidRecord", err)
	}
	if got, _ := store.ListTurns(ctx, "th-z"); len(got) != 0 {
		t.Fatalf("a refused entry left %d turns", len(got))
	}

	js2 := newTestJournal(t)
	raw, _ := json.Marshal(journalPayload{Type: JournalEntryTurnAppend, Turn: &bridge, Provenance: provider.ProvenanceUntrustedSource})
	if _, err := js2.Append(ctx, "th-z", journal.KindAck, bridge.ID, raw); err != nil {
		t.Fatalf("journal append: %v", err)
	}
	fresh := newTestStore(t)
	first, err1 := Resume(ctx, js2, fresh, "th-z")
	second, err2 := Resume(ctx, js2, fresh, "th-z")
	if err1 != nil || err2 != nil || first == 0 || second != 0 {
		t.Fatalf("Resume twice = (%d, %v) then (%d, %v); want applied once, then a no-op", first, err1, second, err2)
	}
	got, _ := fresh.ListTurns(ctx, "th-z")
	if len(got) != 1 || got[0].Origin != OriginBridgeTelegram || got[0].Provenance != provider.ProvenanceUntrustedSource {
		t.Fatalf("marker after a repeated Resume = %+v", got)
	}
}
