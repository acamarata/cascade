package audit

// Purpose: the write-path tests, append and read back, ordering within a
//   single clock tick, concurrent appends under -race, the append-only
//   guarantee (no update or delete path exists, and a racing writer is
//   refused rather than overwriting), and every error path.
// Constraints: Art.7.1 (every file under t.TempDir), Art.7.3 (frozen
//   clock, never the wall clock), Art.11 (no sleeps as synchronization).
// SPORT: internal.audit.Log/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// testInstant is the frozen instant every test clock starts at.
var testInstant = time.Unix(1_700_000_000, 0).UTC()

// newSQLiteStore opens a real SQLite store under t.TempDir. The real
// driver is used rather than an in-memory double wherever the behaviour
// under test is storage behaviour (Art.2 real counterpart).
func newSQLiteStore(t *testing.T) provider.Store {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

// newTestLog returns a Log over a real SQLite store and a real event bus.
func newTestLog(t *testing.T) (*Log, provider.Store, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	store := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })
	return New(store, clock, bus), store, bus, clock
}

// sampleEvent builds a valid event distinguished by n.
func sampleEvent(n int) Event {
	return Event{
		Kind:       KindPolicyDecide,
		Actor:      "user",
		Action:     "read",
		ParamsHash: HashParams([]byte{byte(n)}),
		RiskLevel:  "low",
		Verdict:    "allow",
		Explain:    json.RawMessage(`{"profile":"default"}`),
		Outcome:    "applied",
	}
}

func TestAuditAppend(t *testing.T) {
	ctx := context.Background()
	log, _, bus, _ := newTestLog(t)

	var ids []string
	for i := 1; i <= 3; i++ {
		rec, err := log.Append(ctx, sampleEvent(i))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if rec.Seq != uint64(i) {
			t.Fatalf("Append %d: seq = %d, want %d", i, rec.Seq, i)
		}
		if rec.Time() != testInstant {
			t.Fatalf("Append %d: time = %v, want the injected clock's instant", i, rec.Time())
		}
		ids = append(ids, rec.ID)
	}

	page, err := log.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Records) != 3 {
		t.Fatalf("read back %d records, want 3", len(page.Records))
	}
	for i, rec := range page.Records {
		if rec.Seq != uint64(i+1) || rec.ID != ids[i] {
			t.Fatalf("record %d is %d/%s, want %d/%s: reads must be in insertion order",
				i, rec.Seq, rec.ID, i+1, ids[i])
		}
	}
	if page.Records[0].PrevHash != "" {
		t.Error("the first record links to a predecessor that does not exist")
	}
	if page.Records[1].PrevHash != page.Records[0].Hash {
		t.Error("record 2 does not link to record 1")
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify on an untouched log: %v", err)
	}
	assertBusNotified(ctx, t, bus, ids)
}

// assertBusNotified proves the typed bus notification really fires, and
// that its payload names the record without carrying its body.
func assertBusNotified(ctx context.Context, t *testing.T, bus *events.Bus, ids []string) {
	t.Helper()
	published, err := bus.Replay(ctx, namespace, 0)
	if err != nil {
		t.Fatalf("bus Replay: %v", err)
	}
	if len(published) != len(ids) {
		t.Fatalf("bus carries %d events, want %d", len(published), len(ids))
	}
	for i, ev := range published {
		if ev.Kind != EventKindRecorded {
			t.Errorf("event %d kind = %q, want %q", i, ev.Kind, EventKindRecorded)
		}
		var payload struct {
			ID   string `json:"id"`
			Kind Kind   `json:"kind"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("event %d payload: %v", i, err)
		}
		if payload.ID != ids[i] {
			t.Errorf("event %d names record %q, want %q", i, payload.ID, ids[i])
		}
	}
}

func TestAuditAppendOrderingWithinOneClockTick(t *testing.T) {
	ctx := context.Background()
	log, _, _, clock := newTestLog(t)

	first, err := log.Append(ctx, sampleEvent(1))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	second, err := log.Append(ctx, sampleEvent(2))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if first.TSUnixNano != second.TSUnixNano {
		t.Fatal("the frozen clock moved; this test no longer covers the same-tick case")
	}
	if clock.Now() != testInstant {
		t.Fatal("the clock is not the injected one")
	}
	if first.Seq >= second.Seq {
		t.Fatalf("same-tick records got sequence %d then %d: order must still be defined",
			first.Seq, second.Seq)
	}
	page, err := log.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Records[0].ID != first.ID || page.Records[1].ID != second.ID {
		t.Fatal("same-tick records did not read back in append order")
	}
}

func TestAuditConcurrentOrdering(t *testing.T) {
	ctx := context.Background()
	log, _, _, _ := newTestLog(t)

	const writers = 16
	var wg sync.WaitGroup
	seqs := make([]uint64, writers)
	errs := make([]error, writers)
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			rec, err := log.Append(ctx, sampleEvent(i))
			seqs[i], errs[i] = rec.Seq, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append %d: %v", i, err)
		}
	}
	sorted := append([]uint64(nil), seqs...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	for i, seq := range sorted {
		if seq != uint64(i+1) {
			t.Fatalf("sequence numbers are %v, want a gapless 1..%d: a write was lost", sorted, writers)
		}
	}
	page, err := log.Query(ctx, Filter{Limit: writers})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Records) != writers {
		t.Fatalf("read back %d records, want %d", len(page.Records), writers)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify after concurrent appends: %v", err)
	}
}

// TestAuditAppendOnlySurface pins the public method set. An audit log that
// grows an update or a delete method stops being an audit log, so the
// method set is asserted rather than assumed.
func TestAuditAppendOnlySurface(t *testing.T) {
	want := []string{"Append", "Explain", "Query", "Verify"}
	typ := reflect.TypeOf(&Log{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("*Log exposes %v, want exactly %v: no update or delete path may exist", got, want)
	}
}

// TestAuditAppendRefusesToOverwrite drives the storage-level half of the
// same guarantee: a writer holding a stale view of the tail is refused,
// never allowed to replace the record that already occupies its sequence
// number.
func TestAuditAppendRefusesToOverwrite(t *testing.T) {
	ctx := context.Background()
	log, store, _, clock := newTestLog(t)

	first, err := log.Append(ctx, sampleEvent(1))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	stale := New(store, clock, nil)
	stale.head = head{}
	stale.loaded = true

	if _, err := stale.Append(ctx, sampleEvent(2)); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("a stale writer's append returned %v, want KindConflict", err)
	}
	page, qerr := log.Query(ctx, Filter{})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	if len(page.Records) != 1 || page.Records[0].ID != first.ID {
		t.Fatal("the refused append changed the log")
	}
}
