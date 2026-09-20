package tools

// Purpose (this file): cascade_cpa_search's scan — what matches, in what
//   order, how much of it comes back, and what the excerpt looks like.
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_search (TEST) — P1-E20-W5-S43-T4.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// searchFor runs the tool and returns its hits.
func searchFor(t *testing.T, d *Dispatcher, in string) []SearchHit {
	t.Helper()
	var out SearchOutput
	dispatchJSON(t, d, ToolSearch, in, &out)
	return out.Results
}

// twoThreadService is the fixture most of these tests scan.
func twoThreadService() *fakeConversations {
	return &fakeConversations{
		threads: []ThreadSummary{{ID: "t1"}, {ID: "t2"}},
		details: map[string]ThreadDetail{
			"t1": {ThreadID: "t1", Turns: []TurnRecord{
				{TurnID: "a1", Content: "the migration ledger is idempotent"},
				{TurnID: "a2", Content: "nothing to see"},
				{TurnID: "a3", Content: "MIGRATION again, shouting"},
			}},
			"t2": {ThreadID: "t2", Turns: []TurnRecord{
				{TurnID: "b1", Content: "a migration in another thread"},
			}},
		},
	}
}

func TestSearchIsCaseInsensitive(t *testing.T) {
	d := NewDispatcher(twoThreadService())
	lower := searchFor(t, d, `{"query":"migration"}`)
	upper := searchFor(t, d, `{"query":"MIGRATION"}`)
	if len(lower) != 3 || len(upper) != 3 {
		t.Fatalf("hits = %d lower / %d upper, want 3 each", len(lower), len(upper))
	}
	// A shouted turn must be findable by a quiet query and vice versa.
	if lower[0].TurnID != upper[0].TurnID {
		t.Errorf("case changed the result order: %q vs %q", lower[0].TurnID, upper[0].TurnID)
	}
}

func TestSearchReturnsMostRecentFirstWithinAThread(t *testing.T) {
	d := NewDispatcher(twoThreadService())
	hits := searchFor(t, d, `{"query":"migration"}`)
	// Thread t1's newest matching turn is a3, then a1; t2 follows.
	want := []string{"a3", "a1", "b1"}
	for i, id := range want {
		if hits[i].TurnID != id {
			t.Errorf("hit %d = %q, want %q (newest turn first within each thread)", i, hits[i].TurnID, id)
		}
	}
}

func TestSearchScoreIsConstantAndDeclared(t *testing.T) {
	// A substring scan has no relevance to report. A fabricated gradient
	// would be a ranking nobody computed, and S-44.T3's FTS5 swap is what
	// gives this field meaning later — the shape is stable across it.
	d := NewDispatcher(twoThreadService())
	for _, h := range searchFor(t, d, `{"query":"migration"}`) {
		if h.Score != 1.0 {
			t.Errorf("score for %s = %v, want a constant 1.0", h.TurnID, h.Score)
		}
	}
}

func TestSearchNarrowsToOneThread(t *testing.T) {
	svc := twoThreadService()
	d := NewDispatcher(svc)
	hits := searchFor(t, d, `{"query":"migration","thread_id":"t2"}`)
	if len(hits) != 1 || hits[0].ThreadID != "t2" {
		t.Fatalf("hits = %+v, want the single t2 match", hits)
	}
	// Narrowing must not list every thread first: a scoped search that
	// still enumerated the store would cost the same as an unscoped one.
	for _, id := range svc.gotThread {
		if id != "t2" {
			t.Errorf("a thread-scoped search read thread %q", id)
		}
	}
}

func TestSearchAppliesDefaultAndCeilingLimits(t *testing.T) {
	turns := make([]TurnRecord, 0, 150)
	for i := 0; i < 150; i++ {
		turns = append(turns, TurnRecord{TurnID: string(rune('a'+i%26)) + "-" + itoa(i), Content: "needle"})
	}
	svc := &fakeConversations{
		threads: []ThreadSummary{{ID: "t1"}},
		details: map[string]ThreadDetail{"t1": {ThreadID: "t1", Turns: turns}},
	}
	d := NewDispatcher(svc)
	if got := len(searchFor(t, d, `{"query":"needle"}`)); got != DefaultSearchLimit {
		t.Errorf("hits with no limit = %d, want the default %d", got, DefaultSearchLimit)
	}
	if got := len(searchFor(t, d, `{"query":"needle","limit":5000}`)); got != MaxHistoryLimit {
		t.Errorf("hits with limit 5000 = %d, want the ceiling %d", got, MaxHistoryLimit)
	}
	if got := len(searchFor(t, d, `{"query":"needle","limit":3}`)); got != 3 {
		t.Errorf("hits with limit 3 = %d, want 3", got)
	}
}

func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	// Every turn contains the empty string. Answering would look like a
	// working search that found the whole store.
	d := NewDispatcher(twoThreadService())
	for _, in := range []string{`{"query":""}`, `{"query":"   "}`, `{}`} {
		err := dispatchErr(t, d, ToolSearch, in)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("search(%s): err = %v, want KindInvalidInput", in, err)
		}
	}
}

func TestSearchSkipsAnUnreadableThread(t *testing.T) {
	// One unreadable thread must not make the others unsearchable.
	svc := twoThreadService()
	svc.detailErr = map[string]error{"t1": cascade.New(cascade.KindUnavailable, "gone")}
	d := NewDispatcher(svc)
	hits := searchFor(t, d, `{"query":"migration"}`)
	if len(hits) != 1 || hits[0].ThreadID != "t2" {
		t.Fatalf("hits = %+v, want only the readable thread's match", hits)
	}
}

func TestSearchExcerptMarksWhereItCut(t *testing.T) {
	long := strings.Repeat("x", 200) + "NEEDLE" + strings.Repeat("y", 200)
	svc := &fakeConversations{
		threads: []ThreadSummary{{ID: "t1"}},
		details: map[string]ThreadDetail{"t1": {ThreadID: "t1", Turns: []TurnRecord{{TurnID: "a", Content: long}}}},
	}
	d := NewDispatcher(svc)
	hits := searchFor(t, d, `{"query":"needle"}`)
	ex := hits[0].Excerpt
	if !strings.Contains(ex, "NEEDLE") {
		t.Fatalf("excerpt %q does not contain the match", ex)
	}
	if !strings.HasPrefix(ex, "…") || !strings.HasSuffix(ex, "…") {
		t.Errorf("excerpt %q should be elided at both ends", ex)
	}
	// A short turn is returned whole, with no ellipsis to suggest
	// content that is not there.
	svc.details["t1"] = ThreadDetail{ThreadID: "t1", Turns: []TurnRecord{{TurnID: "b", Content: "just needle"}}}
	if ex := searchFor(t, d, `{"query":"needle"}`)[0].Excerpt; ex != "just needle" {
		t.Errorf("short excerpt = %q, want the whole turn with no ellipsis", ex)
	}
}

func TestSearchExcerptIsValidUTF8(t *testing.T) {
	// The excerpt is cut at a byte offset either side of the match. A
	// turn written in a multi-byte script therefore gets cut mid-rune
	// unless the boundaries are moved to a rune edge, and the result is
	// marshalled as U+FFFD — a replacement character in the operator's
	// own transcript, in place of a letter they wrote.
	// The input is chosen, not guessed. excerptRadius is 60, divisible by
	// 1, 2, 3 and 4, so a run of uniformly-sized runes ALWAYS lands on a
	// boundary and would make this test pass while asserting nothing. The
	// single ASCII "x" below is what knocks the offset off alignment; with
	// it removed, this test passes against the unfixed implementation.
	content := strings.Repeat("日", 200) + "x" + strings.Repeat("日", 19) +
		"needle" + strings.Repeat("🙂", 3) + "y" + strings.Repeat("🙂", 30)
	svc := &fakeConversations{
		threads: []ThreadSummary{{ID: "t1"}},
		details: map[string]ThreadDetail{"t1": {ThreadID: "t1", Turns: []TurnRecord{{TurnID: "a", Content: content}}}},
	}
	d := NewDispatcher(svc)
	ex := searchFor(t, d, `{"query":"needle"}`)[0].Excerpt
	if !utf8.ValidString(ex) {
		t.Fatalf("excerpt is not valid UTF-8: %q", ex)
	}
	if strings.ContainsRune(ex, utf8.RuneError) {
		t.Fatalf("excerpt contains U+FFFD, so a rune was cut in half: %q", ex)
	}
}

// itoa avoids importing strconv for one call in a fixture.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
