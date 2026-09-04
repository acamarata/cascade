package memory

// Purpose: the consolidation job's behaviour tests — what it merges, what
//   it refuses to merge, that a second run is a genuine no-op, and that
//   nothing it retires is left unaccounted for.
// Constraints: every store lives under t.TempDir(); every instant comes
//   from the frozen clock; nothing here reaches the network.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maintenanceSink captures the events a run emitted, in order.
type maintenanceSink struct {
	consolidated []ConsolidatedEvent
	stale        []StaleQueuedEvent
	failWith     error
}

func (s *maintenanceSink) MemoryConsolidated(_ context.Context, ev ConsolidatedEvent) error {
	s.consolidated = append(s.consolidated, ev)
	return s.failWith
}

func (s *maintenanceSink) MemoryStaleQueued(_ context.Context, ev StaleQueuedEvent) error {
	s.stale = append(s.stale, ev)
	return s.failWith
}

// consolidationFixture is a store tree with a consolidator over it.
type consolidationFixture struct {
	base  string
	store *FileStore
	clock *testClockRef
	sink  *maintenanceSink
	c     *Consolidator
}

// newConsolidationFixture builds a fixture rooted in a fresh temp
// directory with a frozen clock (Art.7.1).
func newConsolidationFixture(t *testing.T) *consolidationFixture {
	t.Helper()
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	sink := &maintenanceSink{}
	return &consolidationFixture{
		base: base, store: store, clock: &testClockRef{clk}, sink: sink,
		c: NewConsolidator(base, store, clk, sink),
	}
}

// write stores one record, advancing the clock first so each record gets a
// distinct CreatedAt and the survivor choice is unambiguous.
func (f *consolidationFixture) write(t *testing.T, name, body, description string, kind MemoryKind) {
	t.Helper()
	f.clock.advance(time.Minute)
	e := validEntry()
	e.Name, e.Body, e.Description, e.Kind = name, body, description, kind
	if err := f.store.Write(context.Background(), e); err != nil {
		t.Fatalf("writing %s/%s: %v", kind, name, err)
	}
}

// readConsolidation returns the consolidation record for a survivor.
func (f *consolidationFixture) readConsolidation(t *testing.T, kind MemoryKind, name string) ConsolidationRecord {
	t.Helper()
	path := filepath.Join(f.base, consolidationsDir, string(kind), name+consolidationSuffix)
	data, err := os.ReadFile(path) //nolint:gosec // a path this test itself built
	if err != nil {
		t.Fatalf("reading the consolidation record for %s/%s: %v", kind, name, err)
	}
	var rec ConsolidationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parsing the consolidation record: %v", err)
	}
	return rec
}

// TestConsolidateMemories merges an exact-duplicate group of three into
// one survivor and leaves a complete account of the two it retired.
func TestConsolidateMemories(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "the same words\n", "first phrasing", KindProject)
	f.write(t, "second", "the same words\n", "second phrasing", KindProject)
	f.write(t, "third", "the same words\n", "third phrasing", KindProject)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("ConsolidateMemories: %v", err)
	}
	if report.Merged != 1 || report.Retired != 2 || report.NoChange {
		t.Fatalf("report = %+v, want one group of three merged into one", report)
	}
	if report.Method != ConsolidationMethodExactHash {
		t.Errorf("Method = %q, want %q", report.Method, ConsolidationMethodExactHash)
	}
	// The OLDEST record survives: it is the one the user wrote first.
	if _, err := f.store.Read(ctx, KindProject, "first"); err != nil {
		t.Errorf("the oldest record did not survive: %v", err)
	}
	for _, gone := range []string{"second", "third"} {
		if _, err := f.store.Read(ctx, KindProject, gone); !errors.Is(err, ErrNoSuchEntry) {
			t.Errorf("%s was not retired: %v", gone, err)
		}
	}
	assertEventNames(t, f.sink, "project/first", "project/second", "project/third")
	assertAccountIsComplete(t, f.readConsolidation(t, KindProject, "first"))
}

// assertEventNames checks the single emitted event names the survivor and
// exactly the retired members.
func assertEventNames(t *testing.T, sink *maintenanceSink, survivor string, retired ...string) {
	t.Helper()
	if len(sink.consolidated) != 1 {
		t.Fatalf("emitted %d events, want exactly 1", len(sink.consolidated))
	}
	ev := sink.consolidated[0]
	if ev.ConsolidatedID != survivor {
		t.Errorf("ConsolidatedID = %q, want %q", ev.ConsolidatedID, survivor)
	}
	if len(ev.MemberIDs) != len(retired) {
		t.Fatalf("MemberIDs = %v, want %v", ev.MemberIDs, retired)
	}
	for i, want := range retired {
		if ev.MemberIDs[i] != want {
			t.Errorf("MemberIDs[%d] = %q, want %q", i, ev.MemberIDs[i], want)
		}
	}
}

// assertAccountIsComplete is the "never lose a memory silently" check: the
// record on disk must name every retired member AND carry enough of each
// to reconstruct it.
func assertAccountIsComplete(t *testing.T, rec ConsolidationRecord) {
	t.Helper()
	if len(rec.Members) != 2 {
		t.Fatalf("the account names %d retired records, want 2", len(rec.Members))
	}
	if rec.Body != "the same words\n" {
		t.Errorf("the account kept body %q, so the retired records are not reconstructible", rec.Body)
	}
	wantDescriptions := map[string]string{
		"project/second": "second phrasing",
		"project/third":  "third phrasing",
	}
	for _, m := range rec.Members {
		want, known := wantDescriptions[m.ID]
		if !known {
			t.Errorf("the account names an unexpected record %q", m.ID)
			continue
		}
		if m.Description != want {
			t.Errorf("%s: description = %q, want %q — a retired record's own "+
				"description must survive the merge", m.ID, m.Description, want)
		}
		if m.CreatedAt.IsZero() || m.ContentHash == "" || m.Origin == "" {
			t.Errorf("%s: the account is missing provenance: %+v", m.ID, m)
		}
	}
}

// TestConsolidateIdempotent proves the second run is a genuine no-op: no
// new merges, no events, and no file rewritten.
func TestConsolidateIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)

	if _, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := treeSnapshot(t, f.base)
	f.sink.consolidated = nil

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !report.NoChange || report.Merged != 0 || report.Retired != 0 {
		t.Fatalf("second run report = %+v, want NoChange with nothing merged", report)
	}
	if len(f.sink.consolidated) != 0 {
		t.Errorf("the second run emitted %d events, want none", len(f.sink.consolidated))
	}
	assertTreeUnchanged(t, before, treeSnapshot(t, f.base))
}

// TestConsolidateDistinctContentIsNeverMerged is the safety property that
// matters most: only byte-identical bodies group (R-14.21). Nothing merely
// similar is ever touched.
func TestConsolidateDistinctContentIsNeverMerged(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "one", "the cat sat\n", "a", KindProject)
	f.write(t, "two", "the cat sat.\n", "b", KindProject)
	f.write(t, "three", "the bat sat\n", "c", KindProject)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("ConsolidateMemories: %v", err)
	}
	if report.Merged != 0 || !report.NoChange {
		t.Fatalf("report = %+v, want nothing merged: no two bodies are identical", report)
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, err := f.store.Read(ctx, KindProject, name); err != nil {
			t.Errorf("%s was touched: %v", name, err)
		}
	}
}

// TestConsolidateIsKindScoped proves the group key is kind-scoped:
// identical bodies filed under different kinds are different memories.
func TestConsolidateIsKindScoped(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "shared", "identical\n", "a", KindProject)
	f.write(t, "shared", "identical\n", "b", KindUser)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("ConsolidateMemories: %v", err)
	}
	if report.Merged != 0 {
		t.Fatalf("merged %d group(s) across kinds; a user fact and a project "+
			"note that read the same are two memories", report.Merged)
	}
}

// TestConsolidateNormalizesFormattingOnly checks the normalization is the
// narrow one documented: line endings and trailing space fold, a changed
// word does not.
func TestConsolidateNormalizesFormattingOnly(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "unix", "one\ntwo\n", "a", KindProject)
	f.write(t, "windows", "one\r\ntwo   \r\n\n", "b", KindProject)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("ConsolidateMemories: %v", err)
	}
	if report.Merged != 1 {
		t.Fatalf("report = %+v, want the two line-ending variants merged", report)
	}
}

// TestConsolidateDryRunWritesNothing proves the rehearsal is a rehearsal.
func TestConsolidateDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	before := treeSnapshot(t, f.base)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !report.DryRun || len(report.Groups) != 1 || report.Retired != 0 {
		t.Fatalf("dry-run report = %+v, want one described group and nothing retired", report)
	}
	if len(f.sink.consolidated) != 0 {
		t.Errorf("the dry run emitted %d events, want none", len(f.sink.consolidated))
	}
	assertTreeUnchanged(t, before, treeSnapshot(t, f.base))
}

// TestConsolidateEmbeddingFlagIsRefused proves the default-off embedding
// path is REFUSED rather than silently downgraded to exact-hash grouping.
func TestConsolidateEmbeddingFlagIsRefused(t *testing.T) {
	f := newConsolidationFixture(t)
	_, err := f.c.ConsolidateMemories(context.Background(),
		ConsolidationConfig{EmbeddingEnabled: true})
	if !errors.Is(err, ErrEmbeddingConsolidationUnavailable) {
		t.Fatalf("err = %v, want ErrEmbeddingConsolidationUnavailable", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (ok=%v), want KindUnsupported", kind, ok)
	}
}
