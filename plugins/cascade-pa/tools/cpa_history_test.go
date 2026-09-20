package tools

// Purpose (this file): cascade_cpa_history's two answers — a thread's page
//   of turns, and the thread listing — plus the paging arithmetic both the
//   default and the ceiling depend on.
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_history (TEST) — P1-E20-W5-S43-T4.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// turnsFixture builds n turns, oldest first, with ids turn-1..turn-n.
func turnsFixture(n int) []TurnRecord {
	out := make([]TurnRecord, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, TurnRecord{
			TurnID:  fmt.Sprintf("turn-%d", i),
			Role:    "user",
			Content: fmt.Sprintf("message %d", i),
			Seq:     int64(i),
		})
	}
	return out
}

// historyTurnIDs runs history and returns the turn ids it listed, in order.
func historyTurnIDs(t *testing.T, d *Dispatcher, in string) ([]string, string) {
	t.Helper()
	var out HistoryOutput
	dispatchJSON(t, d, ToolHistory, in, &out)
	ids := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		var row struct {
			TurnID string `json:"turn_id"`
		}
		if err := json.Unmarshal(item, &row); err != nil {
			t.Fatalf("decoding a turn row %s: %v", item, err)
		}
		ids = append(ids, row.TurnID)
	}
	return ids, out.NextCursor
}

func TestResolveLimitDefaultsAndClamps(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, DefaultHistoryLimit},
		{-1, DefaultHistoryLimit},
		{-1000, DefaultHistoryLimit},
		{1, 1},
		{20, 20},
		{100, MaxHistoryLimit},
		{101, MaxHistoryLimit},
		{1_000_000, MaxHistoryLimit},
	}
	for _, c := range cases {
		if got := ResolveLimit(c.in); got != c.want {
			t.Errorf("ResolveLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestHistoryReturnsTheNewestPageOldestFirst(t *testing.T) {
	// Two facts at once, and they pull in opposite directions: the page
	// selected is the RECENT end of the thread (an agent opening a
	// conversation wants its tail), but the turns inside it read
	// oldest-first, because that is the order a conversation reads in.
	svc := &fakeConversations{details: map[string]ThreadDetail{
		"t1": {ThreadID: "t1", Turns: turnsFixture(25)},
	}}
	d := NewDispatcher(svc)

	ids, next := historyTurnIDs(t, d, `{"thread_id":"t1"}`)
	if len(ids) != DefaultHistoryLimit {
		t.Fatalf("page size = %d, want the default %d", len(ids), DefaultHistoryLimit)
	}
	if ids[0] != "turn-6" || ids[len(ids)-1] != "turn-25" {
		t.Errorf("page = %v…%v, want turn-6…turn-25 (newest %d, oldest first)", ids[0], ids[len(ids)-1], DefaultHistoryLimit)
	}
	if next != "turn-6" {
		t.Errorf("next_cursor = %q, want turn-6 (the first turn of this page)", next)
	}
}

func TestHistoryCursorWalksBackwardsAndStops(t *testing.T) {
	svc := &fakeConversations{details: map[string]ThreadDetail{
		"t1": {ThreadID: "t1", Turns: turnsFixture(25)},
	}}
	d := NewDispatcher(svc)

	ids, next := historyTurnIDs(t, d, `{"thread_id":"t1","limit":10}`)
	if ids[0] != "turn-16" || ids[9] != "turn-25" || next != "turn-16" {
		t.Fatalf("page 1 = %v (next %q), want turn-16…turn-25 next turn-16", ids, next)
	}
	ids, next = historyTurnIDs(t, d, `{"thread_id":"t1","limit":10,"before_turn_id":"turn-16"}`)
	if ids[0] != "turn-6" || ids[9] != "turn-15" || next != "turn-6" {
		t.Fatalf("page 2 = %v (next %q), want turn-6…turn-15 next turn-6", ids, next)
	}
	ids, next = historyTurnIDs(t, d, `{"thread_id":"t1","limit":10,"before_turn_id":"turn-6"}`)
	if len(ids) != 5 || ids[0] != "turn-1" || ids[4] != "turn-5" {
		t.Fatalf("page 3 = %v, want the remaining turn-1…turn-5", ids)
	}
	// The walk must END. An empty next_cursor is how a caller knows it
	// has reached the start; a cursor that kept naming turn-1 would loop
	// an agent forever.
	if next != "" {
		t.Errorf("next_cursor after the last page = %q, want empty", next)
	}
}

func TestHistoryClampsAnOversizedLimit(t *testing.T) {
	svc := &fakeConversations{details: map[string]ThreadDetail{
		"t1": {ThreadID: "t1", Turns: turnsFixture(150)},
	}}
	d := NewDispatcher(svc)
	ids, _ := historyTurnIDs(t, d, `{"thread_id":"t1","limit":5000}`)
	if len(ids) != MaxHistoryLimit {
		t.Fatalf("page size = %d, want the ceiling %d", len(ids), MaxHistoryLimit)
	}
}

func TestHistoryUnknownCursorReturnsTheNewestPage(t *testing.T) {
	// The turn may have been retained away since the agent read it.
	// Refusing would strand a caller holding a cursor it cannot repair.
	//
	// The thread is longer than the page on purpose. An earlier version of
	// this test used five turns and a default limit of twenty, so the whole
	// thread came back whatever the cursor did — it would have passed
	// against an implementation that ignored before_turn_id entirely, which
	// is the one thing it exists to rule out. The third assertion below is
	// the one that fails in that case.
	svc := &fakeConversations{details: map[string]ThreadDetail{
		"t1": {ThreadID: "t1", Turns: turnsFixture(25)},
	}}
	d := NewDispatcher(svc)

	noCursor, _ := historyTurnIDs(t, d, `{"thread_id":"t1","limit":5}`)
	unknown, _ := historyTurnIDs(t, d, `{"thread_id":"t1","limit":5,"before_turn_id":"turn-does-not-exist"}`)
	known, _ := historyTurnIDs(t, d, `{"thread_id":"t1","limit":5,"before_turn_id":"turn-21"}`)

	if len(noCursor) != 5 || noCursor[4] != "turn-25" {
		t.Fatalf("no cursor = %v, want the newest five ending at turn-25", noCursor)
	}
	if strings.Join(unknown, ",") != strings.Join(noCursor, ",") {
		t.Errorf("unknown cursor = %v, want the same page as no cursor at all (%v)", unknown, noCursor)
	}
	if strings.Join(known, ",") == strings.Join(noCursor, ",") {
		t.Fatalf("a KNOWN cursor returned the same page as none (%v); before_turn_id is being ignored", known)
	}
	if known[0] != "turn-16" || known[4] != "turn-20" {
		t.Errorf("known cursor page = %v, want turn-16…turn-20", known)
	}
}

func TestHistoryWithoutAThreadIDListsThreads(t *testing.T) {
	svc := &fakeConversations{threads: []ThreadSummary{
		{ID: "t1", Title: "first", UpdatedAt: "2026-09-20T10:00:00Z"},
		{ID: "t2", Title: "second", UpdatedAt: "2026-09-20T11:00:00Z"},
	}}
	d := NewDispatcher(svc)

	var out HistoryOutput
	dispatchJSON(t, d, ToolHistory, `{}`, &out)
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2 thread rows", len(out.Items))
	}
	var row map[string]string
	if err := json.Unmarshal(out.Items[0], &row); err != nil {
		t.Fatalf("decoding a thread row: %v", err)
	}
	// The three keys the schema names, and only those.
	for _, k := range []string{"id", "title", "updated_at"} {
		if _, ok := row[k]; !ok {
			t.Errorf("thread row %v is missing %q", row, k)
		}
	}
	if len(row) != 3 {
		t.Errorf("thread row = %v, want exactly id/title/updated_at", row)
	}
	if out.NextCursor != "" {
		t.Errorf("next_cursor = %q on a thread listing, want empty; the listing is not paged", out.NextCursor)
	}
}

func TestHistoryEmptyThreadListingIsAnEmptyListNotNull(t *testing.T) {
	// `"items": null` and `"items": []` are different documents, and a
	// client iterating the first crashes where the second is a no-op.
	d := NewDispatcher(&fakeConversations{})
	raw, err := d.Dispatch(t.Context(), ToolHistory, []byte(`{}`))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if got := string(raw); got != `{"items":[]}` {
		t.Errorf("empty listing = %s, want {\"items\":[]}", got)
	}
}

func TestHistoryPropagatesTheServiceError(t *testing.T) {
	d := NewDispatcher(&fakeConversations{
		threadsErr: cascade.New(cascade.KindUnavailable, "daemon not running"),
	})
	if err := dispatchErr(t, d, ToolHistory, `{}`); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("listing failure: err = %v, want KindUnavailable preserved", err)
	}
	d = NewDispatcher(&fakeConversations{})
	if err := dispatchErr(t, d, ToolHistory, `{"thread_id":"nope"}`); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown thread: err = %v, want KindNotFound preserved", err)
	}
}
