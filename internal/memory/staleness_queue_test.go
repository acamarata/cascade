package memory

// Purpose: the staleness queue file's codec and the scan's edge cases —
//   the refusals that keep an unreadable queue from being read as an empty
//   one, and the age rule's fallbacks. Split from staleness_test.go under
//   the 300-line file cap.
// Constraints: every store lives under t.TempDir(); the frozen clock
//   supplies every instant; nothing here reaches the network.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestStalenessSetUnknownVersionIsRefused proves the
// forward-compatibility refusal is distinct from a damaged file.
func TestStalenessSetUnknownVersionIsRefused(t *testing.T) {
	_, err := decodeStalenessSet([]byte(`{"format":99,"kind":"project","ids":[]}`))
	if !errors.Is(err, ErrUnsupportedStalenessFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedStalenessFormat", err)
	}
}

// TestStalenessSetMalformedIsRefused proves a damaged or kindless set is
// refused rather than read as an empty queue, which would re-announce
// every already-queued record.
func TestStalenessSetMalformedIsRefused(t *testing.T) {
	if _, err := decodeStalenessSet([]byte(`{ not json`)); !errors.Is(err, ErrMalformedStalenessSet) {
		t.Errorf("err = %v, want ErrMalformedStalenessSet for a damaged file", err)
	}
	if _, err := decodeStalenessSet([]byte(`{"format":1,"kind":"nonsense"}`)); !errors.Is(err, ErrMalformedStalenessSet) {
		t.Errorf("err = %v, want ErrMalformedStalenessSet for an unknown kind", err)
	}
}

// TestIsStaleUsesCreatedAtWhenNeverUpdated proves the documented fallback:
// a record with no UpdatedAt is judged on its creation instant.
func TestIsStaleUsesCreatedAtWhenNeverUpdated(t *testing.T) {
	now := fixedNow.Add(90 * 24 * time.Hour)
	var e MemoryEntry
	e.Provenance.CreatedAt = fixedNow
	if !isStale(e, now, DefaultStalenessWindow) {
		t.Error("a record with only a CreatedAt was not judged on it")
	}
	var undated MemoryEntry
	if isStale(undated, now, DefaultStalenessWindow) {
		t.Error("a record with no timestamps at all was flagged; it has no age to judge")
	}
}

// TestMaintenanceEventNames pins the two bus names. They are the strings a
// subscriber matches on, so a rename is a wire change and must fail here
// rather than in a subscriber that quietly stops hearing anything.
func TestMaintenanceEventNames(t *testing.T) {
	if got := (ConsolidatedEvent{}).EventName(); got != "memory.consolidated" {
		t.Errorf("ConsolidatedEvent.EventName = %q", got)
	}
	if got := (StaleQueuedEvent{}).EventName(); got != "memory.stale_queued" {
		t.Errorf("StaleQueuedEvent.EventName = %q", got)
	}
}

// TestNoSinkIsASupportedConfiguration proves a job built with no event
// bus runs and stores exactly the same things — discarding an event never
// changes what is retired or queued.
func TestNoSinkIsASupportedConfiguration(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	seedPair(t, store)

	report, err := NewConsolidator(base, store, clk, nil).
		ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil || report.Merged != 1 {
		t.Fatalf("consolidating with no sink: report=%+v err=%v", report, err)
	}
	clk.Advance(40 * 24 * time.Hour)
	scan, err := NewStalenessScanner(base, store, clk, nil).
		ScanStaleness(ctx, StalenessConfig{})
	if err != nil || scan.Queued != 1 {
		t.Fatalf("scanning with no sink: report=%+v err=%v", scan, err)
	}
}

// TestStalenessQueueReadFailureIsReturned proves an unreadable queue file
// is a refusal, never a silent empty set: reading it as empty would
// re-announce every already-queued record.
func TestStalenessQueueReadFailureIsReturned(t *testing.T) {
	base := t.TempDir()
	clk := newTestClock()
	broken := newFailingFS()
	broken.failRead = true
	scanner := newStalenessScannerWithFS(base, NewFileStore(base, clk), clk, nil, broken)
	if _, err := scanner.loadQueue(KindProject); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("err = %v, want ErrStoreIO", err)
	}
}
