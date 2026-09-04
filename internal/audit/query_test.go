package audit

// Purpose: the read-surface tests, filtering by each supported field and
//   in combination, boundary and empty results, cursor pagination, the
//   fail-closed refusals, and explain-why including its unknown-id path.
// Constraints: Art.7.1, Art.7.3 (the frozen clock is advanced explicitly
//   so time-range assertions are exact, never wall-clock dependent).
// SPORT: internal.audit.Log/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// seedLog appends one record per spec, advancing the clock a second
// between each so time-range filters have distinct instants to select on.
func seedLog(t *testing.T, specs []Event) (*Log, []Record) {
	t.Helper()
	ctx := context.Background()
	log, _, _, clock := newTestLog(t)
	var recs []Record
	for i, ev := range specs {
		if i > 0 {
			clock.Advance(time.Second)
		}
		rec, err := log.Append(ctx, ev)
		if err != nil {
			t.Fatalf("seeding record %d: %v", i, err)
		}
		recs = append(recs, rec)
	}
	return log, recs
}

// mixedEvents is the seed corpus: three kinds, two actors, two verdicts.
func mixedEvents() []Event {
	return []Event{
		{Kind: KindPolicyDecide, Actor: "alice", Action: "read", Verdict: "allow"},
		{Kind: KindPolicyRoute, Actor: "bob", Action: "route", Verdict: "deny"},
		{Kind: KindConfigReload, Actor: "alice", Action: "reload", Verdict: "allow"},
		{Kind: KindElevationDeny, Actor: "bob", Action: "elevate", Verdict: "deny"},
	}
}

func TestAuditQuery(t *testing.T) {
	ctx := context.Background()
	log, recs := seedLog(t, mixedEvents())

	cases := map[string]struct {
		filter   Filter
		wantSeqs []uint64
	}{
		"no filter":       {Filter{}, []uint64{1, 2, 3, 4}},
		"by kind":         {Filter{Kinds: []Kind{KindPolicyDecide}}, []uint64{1}},
		"by two kinds":    {Filter{Kinds: []Kind{KindPolicyRoute, KindConfigReload}}, []uint64{2, 3}},
		"by actor":        {Filter{Actors: []string{"alice"}}, []uint64{1, 3}},
		"by verdict":      {Filter{Verdicts: []string{"deny"}}, []uint64{2, 4}},
		"actor + verdict": {Filter{Actors: []string{"bob"}, Verdicts: []string{"deny"}}, []uint64{2, 4}},
		"contradiction":   {Filter{Actors: []string{"alice"}, Verdicts: []string{"deny"}}, nil},
		"no such actor":   {Filter{Actors: []string{"nobody"}}, nil},
		"since is inclusive": {
			Filter{Since: recs[1].Time()}, []uint64{2, 3, 4},
		},
		"until is exclusive": {
			Filter{Until: recs[2].Time()}, []uint64{1, 2},
		},
		"window": {
			Filter{Since: recs[1].Time(), Until: recs[3].Time()}, []uint64{2, 3},
		},
	}
	for name, tc := range cases {
		page, err := log.Query(ctx, tc.filter)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var got []uint64
		for _, rec := range page.Records {
			got = append(got, rec.Seq)
		}
		if len(got) != len(tc.wantSeqs) {
			t.Errorf("%s: got sequences %v, want %v", name, got, tc.wantSeqs)
			continue
		}
		for i := range got {
			if got[i] != tc.wantSeqs[i] {
				t.Errorf("%s: got sequences %v, want %v", name, got, tc.wantSeqs)
				break
			}
		}
		if page.NextCursor != "" {
			t.Errorf("%s: a complete page still offered a cursor", name)
		}
	}
}

// TestAuditQueryFailsClosed is the disclosure test: every filter the
// package cannot honour exactly must refuse and return nothing. The
// failure this guards against is a malformed filter degrading into "match
// all" and handing back records the caller never asked to see.
func TestAuditQueryFailsClosed(t *testing.T) {
	ctx := context.Background()
	log, _ := seedLog(t, mixedEvents())

	cases := map[string]Filter{
		"unknown kind":        {Kinds: []Kind{Kind("policy.invent")}},
		"empty kind string":   {Kinds: []Kind{Kind("")}},
		"present-empty kinds": {Kinds: []Kind{}},
		"present-empty actor": {Actors: []string{}},
		"empty actor entry":   {Actors: []string{""}},
		"present-empty verd":  {Verdicts: []string{}},
		"empty verdict entry": {Verdicts: []string{""}},
		"inverted window":     {Since: testInstant.Add(time.Hour), Until: testInstant},
		"empty window":        {Since: testInstant, Until: testInstant},
		"negative limit":      {Limit: -1},
		"oversize limit":      {Limit: maxLimit + 1},
		"malformed cursor":    {Cursor: "not-a-cursor"},
		"empty-body cursor":   {Cursor: cursorPrefix},
		"non-numeric cursor":  {Cursor: cursorPrefix + "abc"},
	}
	for name, f := range cases {
		page, err := log.Query(ctx, f)
		if err == nil {
			t.Errorf("%s: Query accepted the filter and returned %d records", name, len(page.Records))
			continue
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: kind = %v, want KindInvalidInput", name, err)
		}
		if len(page.Records) != 0 {
			t.Errorf("%s: a refused query still returned %d records", name, len(page.Records))
		}
	}
}

func TestAuditParseFilter(t *testing.T) {
	f, err := ParseFilter([]string{
		"kind=policy.decide", "actor=alice", "verdict=allow",
		"since=2026-01-01T00:00:00Z", "until=2026-01-02T00:00:00Z",
		"limit=10", "cursor=" + cursorPrefix + "3",
	})
	if err != nil {
		t.Fatalf("ParseFilter: %v", err)
	}
	if len(f.Kinds) != 1 || f.Kinds[0] != KindPolicyDecide || f.Actors[0] != "alice" ||
		f.Verdicts[0] != "allow" || f.Limit != 10 || f.Cursor != cursorPrefix+"3" {
		t.Fatalf("ParseFilter produced %+v", f)
	}
	if f.Since.IsZero() || f.Until.IsZero() {
		t.Fatal("ParseFilter dropped the time bounds")
	}
	bad := map[string][]string{
		"unknown field":   {"nonsense=1"},
		"unknown field 2": {"kind=policy.decide", "user=alice"},
		"no separator":    {"kind"},
		"bad limit":       {"limit=lots"},
		"bad since":       {"since=yesterday"},
		"bad until":       {"until=2026-13-45"},
	}
	for name, tokens := range bad {
		got, err := ParseFilter(tokens)
		if err == nil {
			t.Errorf("%s: ParseFilter accepted %v", name, tokens)
			continue
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: kind = %v, want KindInvalidInput", name, err)
		}
		if got.Kinds != nil || got.Actors != nil {
			t.Errorf("%s: a refused parse still produced constraints %+v", name, got)
		}
	}
}

func TestAuditCursorPagination(t *testing.T) {
	ctx := context.Background()
	var specs []Event
	for i := 0; i < 7; i++ {
		specs = append(specs, sampleEvent(i))
	}
	log, _ := seedLog(t, specs)

	var seen []uint64
	cursor := ""
	for page := 0; page < 4; page++ {
		got, err := log.Query(ctx, Filter{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, rec := range got.Records {
			seen = append(seen, rec.Seq)
		}
		cursor = got.NextCursor
		if cursor == "" {
			break
		}
	}
	if cursor != "" {
		t.Fatal("pagination did not terminate")
	}
	if len(seen) != 7 {
		t.Fatalf("paged through %d records, want 7: %v", len(seen), seen)
	}
	for i, seq := range seen {
		if seq != uint64(i+1) {
			t.Fatalf("pages returned %v, want a gapless 1..7 with no repeats", seen)
		}
	}
}

func TestAuditExplain(t *testing.T) {
	ctx := context.Background()
	log, _, _, _ := newTestLog(t)

	ev := sampleEvent(1)
	ev.Explain = json.RawMessage(`{"profile":"strict","overlays":["team"],"denylist_hit":"rm -rf"}`)
	ev.PolicySnapshot = json.RawMessage(`{"version":7,"default":"deny"}`)
	rec, err := log.Append(ctx, ev)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := log.Explain(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if got.Record.ID != rec.ID || got.Record.Seq != rec.Seq {
		t.Fatalf("Explain returned record %s/%d, want %s/%d", got.Record.ID, got.Record.Seq, rec.ID, rec.Seq)
	}
	var explain struct {
		Profile     string   `json:"profile"`
		Overlays    []string `json:"overlays"`
		DenylistHit string   `json:"denylist_hit"`
	}
	if err := json.Unmarshal(got.Explain, &explain); err != nil {
		t.Fatalf("explain body: %v", err)
	}
	if explain.Profile != "strict" || explain.DenylistHit != "rm -rf" || len(explain.Overlays) != 1 {
		t.Fatalf("explain body did not survive the round trip: %+v", explain)
	}
	if string(got.PolicySnapshot) != `{"version":7,"default":"deny"}` {
		t.Fatalf("policy snapshot = %s, want the snapshot recorded at decision time", got.PolicySnapshot)
	}
}

func TestAuditExplainUnknownID(t *testing.T) {
	ctx := context.Background()
	log, _, _, _ := newTestLog(t)
	if _, err := log.Append(ctx, sampleEvent(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for name, id := range map[string]string{
		"never recorded": "01ZZZZZZZZZZZZZZZZZZZZZZZZ",
		"empty":          "",
	} {
		got, err := log.Explain(ctx, id)
		if err == nil {
			t.Errorf("%s: Explain returned an explanation for %q", name, id)
			continue
		}
		if len(got.Explain) != 0 || got.Record.Seq != 0 {
			t.Errorf("%s: a refused Explain still returned content", name)
		}
	}
	if _, err := log.Explain(ctx, "01ZZZZZZZZZZZZZZZZZZZZZZZZ"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown id: %v, want KindNotFound", err)
	}
	if _, err := log.Explain(ctx, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty id: %v, want KindInvalidInput", err)
	}
}
