package memory

// Purpose: the staleness scan's behaviour tests — what it flags, that a
//   second scan is a genuine no-op, that a refreshed record LEAVES the
//   queue, and that nothing it flags is ever retired.
// Constraints: every store lives under t.TempDir(); every instant comes
//   from the frozen clock; nothing here reaches the network.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stalenessFixture is a store tree with a scanner over it.
type stalenessFixture struct {
	base  string
	store *FileStore
	clock *testClockRef
	sink  *maintenanceSink
	s     *StalenessScanner
}

// newStalenessFixture builds a fixture rooted in a fresh temp directory
// with a frozen clock (Art.7.1).
func newStalenessFixture(t *testing.T) *stalenessFixture {
	t.Helper()
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	sink := &maintenanceSink{}
	return &stalenessFixture{
		base: base, store: store, clock: &testClockRef{clk}, sink: sink,
		s: NewStalenessScanner(base, store, clk, sink),
	}
}

// write stores one record at the clock's current instant.
func (f *stalenessFixture) write(t *testing.T, name, body string) {
	t.Helper()
	e := validEntry()
	e.Name, e.Body, e.Kind = name, body, KindProject
	if err := f.store.Write(context.Background(), e); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// queue reads a kind's stored staleness set straight off the disk.
func (f *stalenessFixture) queue(t *testing.T, kind MemoryKind) []string {
	t.Helper()
	path := filepath.Join(f.base, stalenessDir, string(kind)+stalenessSuffix)
	data, err := os.ReadFile(path) //nolint:gosec // a path this test itself built
	if err != nil {
		t.Fatalf("reading the staleness set for %s: %v", kind, err)
	}
	var set stalenessSet
	if err := json.Unmarshal(data, &set); err != nil {
		t.Fatalf("parsing the staleness set: %v", err)
	}
	return set.IDs
}

// TestScanStaleness flags records past the window and leaves recent ones
// alone.
func TestScanStaleness(t *testing.T) {
	ctx := context.Background()
	f := newStalenessFixture(t)
	f.write(t, "old-one", "a\n")
	f.write(t, "old-two", "b\n")
	f.clock.advance(40 * 24 * time.Hour)
	f.write(t, "fresh", "c\n")

	report, err := f.s.ScanStaleness(ctx, StalenessConfig{})
	if err != nil {
		t.Fatalf("ScanStaleness: %v", err)
	}
	if report.Queued != 2 || report.Total != 2 || report.Idempotent {
		t.Fatalf("report = %+v, want two records queued", report)
	}
	if report.WindowDays != 30 {
		t.Errorf("WindowDays = %v, want the documented 30-day default", report.WindowDays)
	}
	got := strings.Join(f.queue(t, KindProject), ",")
	if got != "project/old-one,project/old-two" {
		t.Fatalf("the queue holds %q, want the two aged records in lexical order", got)
	}
	if len(f.sink.stale) != 1 || len(f.sink.stale[0].StaleIDs) != 2 {
		t.Fatalf("emitted %+v, want one event naming both records", f.sink.stale)
	}
	// The whole point: flagging never retires. Both records are still there.
	for _, name := range []string{"old-one", "old-two", "fresh"} {
		if _, err := f.store.Read(ctx, KindProject, name); err != nil {
			t.Errorf("%s was touched by a scan that may only flag: %v", name, err)
		}
	}
}

// TestScanStalenessIdempotent proves the second scan is a genuine no-op:
// nothing queued, nothing emitted, no file rewritten.
func TestScanStalenessIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newStalenessFixture(t)
	f.write(t, "old-one", "a\n")
	f.clock.advance(40 * 24 * time.Hour)

	if _, err := f.s.ScanStaleness(ctx, StalenessConfig{}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	before := treeSnapshot(t, f.base)
	f.sink.stale = nil

	report, err := f.s.ScanStaleness(ctx, StalenessConfig{})
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if !report.Idempotent || report.Queued != 0 || report.Dropped != 0 {
		t.Fatalf("second scan report = %+v, want Idempotent with nothing queued", report)
	}
	if len(f.sink.stale) != 0 {
		t.Errorf("the second scan emitted %d events, want none", len(f.sink.stale))
	}
	assertTreeUnchanged(t, before, treeSnapshot(t, f.base))
}

// TestScanStalenessIsReversible is the requirement that staleness must not
// harden into a verdict: a record that is updated LEAVES the queue on the
// next scan, with no manual step.
func TestScanStalenessIsReversible(t *testing.T) {
	ctx := context.Background()
	f := newStalenessFixture(t)
	f.write(t, "aging", "a\n")
	f.clock.advance(40 * 24 * time.Hour)
	if _, err := f.s.ScanStaleness(ctx, StalenessConfig{}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := f.queue(t, KindProject); len(got) != 1 {
		t.Fatalf("the queue holds %v, want the aged record", got)
	}

	// The user comes back and rewrites the record.
	f.write(t, "aging", "a revised body\n")

	report, err := f.s.ScanStaleness(ctx, StalenessConfig{})
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if report.Dropped != 1 || report.Total != 0 {
		t.Fatalf("report = %+v, want the refreshed record dropped from the queue", report)
	}
	if got := f.queue(t, KindProject); len(got) != 0 {
		t.Fatalf("the queue still holds %v after the record was updated", got)
	}
}

// TestScanStalenessEmptyStore proves an empty store is a clean no-op, not
// an error and not an event.
func TestScanStalenessEmptyStore(t *testing.T) {
	f := newStalenessFixture(t)
	report, err := f.s.ScanStaleness(context.Background(), StalenessConfig{})
	if err != nil {
		t.Fatalf("ScanStaleness on an empty store: %v", err)
	}
	if report.Queued != 0 || report.Total != 0 || !report.Idempotent {
		t.Fatalf("report = %+v, want an empty, idempotent result", report)
	}
	if len(f.sink.stale) != 0 {
		t.Errorf("an empty store emitted %d events, want none", len(f.sink.stale))
	}
}

// TestScanStalenessHonoursTheConfiguredWindow proves the window is the
// configured one, not a hard-coded month.
func TestScanStalenessHonoursTheConfiguredWindow(t *testing.T) {
	ctx := context.Background()
	f := newStalenessFixture(t)
	f.write(t, "recent", "a\n")
	f.clock.advance(2 * 24 * time.Hour)

	loose, err := f.s.ScanStaleness(ctx, StalenessConfig{Window: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("loose window: %v", err)
	}
	if loose.Total != 0 {
		t.Fatalf("a two-day-old record was flagged against a 30-day window: %+v", loose)
	}
	tight, err := f.s.ScanStaleness(ctx, StalenessConfig{Window: 24 * time.Hour})
	if err != nil {
		t.Fatalf("tight window: %v", err)
	}
	if tight.Total != 1 || tight.Queued != 1 {
		t.Fatalf("report = %+v, want the record flagged against a one-day window", tight)
	}
}

// TestScanStalenessZeroWindowUsesTheDefault proves an unset window means
// the documented default, never "everything is stale".
func TestScanStalenessZeroWindowUsesTheDefault(t *testing.T) {
	f := newStalenessFixture(t)
	f.write(t, "brand-new", "a\n")
	report, err := f.s.ScanStaleness(context.Background(), StalenessConfig{Window: -1})
	if err != nil {
		t.Fatalf("ScanStaleness: %v", err)
	}
	if report.Total != 0 {
		t.Fatalf("a record written this instant was flagged: %+v", report)
	}
}

// TestScanStalenessDamagedRecordIsReported proves one bad file neither
// fails the scan nor gets queued.
func TestScanStalenessDamagedRecordIsReported(t *testing.T) {
	ctx := context.Background()
	f := newStalenessFixture(t)
	f.write(t, "old-one", "a\n")
	f.clock.advance(40 * 24 * time.Hour)
	damaged := filepath.Join(f.base, string(KindProject), "broken.md")
	if err := os.WriteFile(damaged, []byte("not a memory record"), 0o600); err != nil {
		t.Fatalf("seeding a damaged file: %v", err)
	}

	report, err := f.s.ScanStaleness(ctx, StalenessConfig{})
	if err != nil {
		t.Fatalf("one damaged file failed the whole scan: %v", err)
	}
	if len(report.Unreadable) != 1 || report.Unreadable[0] != "project/broken" {
		t.Errorf("Unreadable = %v, want the damaged record named", report.Unreadable)
	}
	if report.Total != 1 {
		t.Errorf("report = %+v, want only the healthy aged record queued", report)
	}
}

// TestScanStalenessSkipsWhileAnotherScanIsInFlight proves the in-process
// guard stands down rather than racing.
func TestScanStalenessSkipsWhileAnotherScanIsInFlight(t *testing.T) {
	f := newStalenessFixture(t)
	f.s.running.Lock()
	defer f.s.running.Unlock()

	report, err := f.s.ScanStaleness(context.Background(), StalenessConfig{})
	if err != nil {
		t.Fatalf("a skipped scan returned an error: %v", err)
	}
	if !report.Skipped || report.Queued != 0 {
		t.Fatalf("report = %+v, want Skipped with nothing queued", report)
	}
}

// TestScanStalenessCanceledContextIsRefused proves the scan does not start
// work on a context that is already done.
func TestScanStalenessCanceledContextIsRefused(t *testing.T) {
	f := newStalenessFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.s.ScanStaleness(ctx, StalenessConfig{}); err == nil {
		t.Fatal("a canceled context produced no error")
	}
}

// TestScanStalenessWriteFailureIsReturned proves a file-system fault is a
// typed refusal rather than a silently skipped queue write.
func TestScanStalenessWriteFailureIsReturned(t *testing.T) {
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	e := validEntry()
	e.Name, e.Kind = "aged", KindProject
	if err := store.Write(context.Background(), e); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	clk.Advance(40 * 24 * time.Hour)

	broken := newFailingFS()
	broken.failWrite = true
	scanner := newStalenessScannerWithFS(base, store, clk, nil, broken)
	if _, err := scanner.ScanStaleness(context.Background(), StalenessConfig{}); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("err = %v, want ErrStoreIO", err)
	}
}
