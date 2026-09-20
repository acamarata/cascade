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

func TestSearchUsesTheIndexWhenOneExists(t *testing.T) {
	// The indexed path is the point of S-44.T3: the FTS5 backend replaces
	// the scan. What must survive the swap is the {results} shape, and the
	// excerpt must still be cut by this package (which knows not to halve a
	// rune) rather than by the daemon.
	svc := twoThreadService()
	svc.searchResults = []SearchResult{
		{ThreadID: "t9", TurnID: "z1", Content: "the migration ledger is idempotent", Score: 4.5},
		{ThreadID: "t9", TurnID: "z2", Content: "nothing relevant here", Score: 1.25},
	}
	d := NewDispatcher(svc)
	hits := searchFor(t, d, `{"query":"migration","limit":7}`)

	if len(hits) != 2 {
		t.Fatalf("hits = %d, want the index's two rows", len(hits))
	}
	if hits[0].TurnID != "z1" || hits[0].ThreadID != "t9" {
		t.Errorf("hit 0 = %+v, want the index's own first row", hits[0])
	}
	// The backend's relevance, not a constant: a swap that kept score 1.0
	// would have thrown away the only thing FTS5 adds over the scan.
	if hits[0].Score != 4.5 || hits[1].Score != 1.25 {
		t.Errorf("scores = %v/%v, want the backend's own", hits[0].Score, hits[1].Score)
	}
	if hits[0].Excerpt != "the migration ledger is idempotent" {
		t.Errorf("excerpt = %q, want it cut from the indexed content", hits[0].Excerpt)
	}
	// A hit whose content does not contain the literal query still gets an
	// excerpt: FTS5 matches stems and tokens a substring search cannot find.
	// Asserting only "not empty" would pass on any string at all, so this
	// pins what the excerpt must be — the head of the indexed content.
	if hits[1].Excerpt != "nothing relevant here" {
		t.Errorf("stem-match excerpt = %q, want the head of the indexed content", hits[1].Excerpt)
	}
	// The request must carry the caller's scope and bound to the backend,
	// rather than the backend being asked for everything and trimmed here.
	if len(svc.gotSearch) != 1 || svc.gotSearch[0].Query != "migration" || svc.gotSearch[0].Limit != 7 {
		t.Errorf("Search called with %+v, want the query and limit passed through", svc.gotSearch)
	}
}

func TestSearchFallsBackToTheScanWithNoIndex(t *testing.T) {
	// A store with no FTS5 index must not report "no matches" — that would
	// tell an agent its operator never wrote the words in front of them.
	svc := twoThreadService()
	svc.searchErr = ErrSearchUnavailable
	d := NewDispatcher(svc)
	hits := searchFor(t, d, `{"query":"migration"}`)
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want the scan's three matches", len(hits))
	}
	if hits[0].Score != 1.0 {
		t.Errorf("scan score = %v, want the scan's constant 1.0", hits[0].Score)
	}
}

func TestSearchReportsAFailedIndexRatherThanEmptyResults(t *testing.T) {
	// "the search did not run" and "nothing matched" are different facts.
	// Returning an empty result set for the first is the failure mode this
	// whole package is careful about elsewhere.
	svc := twoThreadService()
	svc.searchErr = cascade.New(cascade.KindUnavailable, "daemon not running")
	d := NewDispatcher(svc)
	err := dispatchErr(t, d, ToolSearch, `{"query":"migration"}`)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("index failure: err = %v, want KindUnavailable propagated", err)
	}
}
