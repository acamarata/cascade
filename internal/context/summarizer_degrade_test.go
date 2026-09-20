package context

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the degrade-gracefully contract and its fail-closed edge --
//   GetSummary never blocks the compose path on a regeneration failure, and
//   Summarize never HANDS a stale or failed summary to the Composer as
//   content. Plus the events an operator sees for each case.
// SPORT: context-engine/summarizer-degrade-test (ADD, P1-E21-W5-S46-T2).

// TestSummarizerGetSummaryModelExecuteFailureNoExisting: a regeneration
// failure with no prior stored record degrades to last-valid-or-nil (nil
// here) plus a SummarizationFailedEvent naming the dispatch -- never a
// returned error.
func TestSummarizerGetSummaryModelExecuteFailureNoExisting(t *testing.T) {
	exec := &fakeExecutor{err: cascade.New(cascade.KindUnavailable, "model unreachable")}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())

	outcome, err := s.GetSummary(context.Background(), "entity-1", GranularityEpoch, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: want nil error on a degrade-gracefully path, got %v", err)
	}
	if outcome.Record.Content != "" {
		t.Errorf("Record.Content = %q, want empty (no prior summary existed)", outcome.Record.Content)
	}
	if outcome.Failed == nil {
		t.Fatal("outcome.Failed = nil, want a SummarizationFailedEvent")
	}
	for _, want := range []string{"unavailable", "dispatch failed", "model unreachable"} {
		if !strings.Contains(outcome.Failed.Reason, want) {
			t.Errorf("Failed.Reason = %q, want substring %q", outcome.Failed.Reason, want)
		}
	}
	if outcome.Stale || outcome.Warning != nil {
		t.Errorf("outcome.Stale=%v outcome.Warning=%v, want both zero (nothing to degrade FROM)", outcome.Stale, outcome.Warning)
	}
}

// TestSummarizerGetSummaryModelExecuteFailureWithExisting: a regeneration
// failure with a prior valid record returns that record marked Stale, plus
// both events, and the warning names the version the stored summary actually
// describes.
func TestSummarizerGetSummaryModelExecuteFailureWithExisting(t *testing.T) {
	exec := &fakeExecutor{output: "good summary"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	ctx := context.Background()

	if _, err := s.GetSummary(ctx, "entity-1", GranularityThread, "content v1", "v1"); err != nil {
		t.Fatalf("seeding GetSummary: %v", err)
	}
	exec.err = cascade.New(cascade.KindUnavailable, "model unreachable")

	outcome, err := s.GetSummary(ctx, "entity-1", GranularityThread, "content v2", "v2")
	if err != nil {
		t.Fatalf("GetSummary: want nil error on a degrade-gracefully path, got %v", err)
	}
	if !outcome.Stale {
		t.Error("outcome.Stale = false, want true")
	}
	if outcome.Record.Content != "good summary" {
		t.Errorf("Record.Content = %q, want the last valid %q", outcome.Record.Content, "good summary")
	}
	if outcome.Failed == nil || outcome.Warning == nil {
		t.Fatalf("outcome.Failed=%v outcome.Warning=%v, want both non-nil", outcome.Failed, outcome.Warning)
	}
	if !strings.Contains(outcome.Warning.Reason, "v1") {
		t.Errorf("Warning.Reason = %q, want it to name the source version the stored summary describes", outcome.Warning.Reason)
	}
}

// TestSummarizerGetSummaryStorageGetFailure: a Store.Get failure that is
// NOT KindNotFound surfaces as a returned error (never silently treated as
// "no record").
func TestSummarizerGetSummaryStorageGetFailure(t *testing.T) {
	exec := &fakeExecutor{output: "x"}
	store := &flakyStore{MemStore: storetest.NewMemStore(), getErr: cascade.New(cascade.KindUnavailable, "disk unavailable")}
	s := newTestSummarizer(t, exec, store)

	_, err := s.GetSummary(context.Background(), "entity-1", GranularityThread, "content", "v1")
	if err == nil {
		t.Fatal("GetSummary: want error on a real storage failure")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Errorf("error kind = %v (ok=%v), want unavailable", k, ok)
	}
}

// TestSummarizerGetSummaryCorruptStoredRecord: malformed JSON in the store
// surfaces as a KindIntegrity error, never a panic.
func TestSummarizerGetSummaryCorruptStoredRecord(t *testing.T) {
	exec := &fakeExecutor{output: "x"}
	store := storetest.NewMemStore()
	if err := store.Put(context.Background(), summarizerNamespace, summaryKey("entity-1", GranularityThread), []byte("not json")); err != nil {
		t.Fatalf("seeding corrupt record: %v", err)
	}
	s := newTestSummarizer(t, exec, store)

	_, err := s.GetSummary(context.Background(), "entity-1", GranularityThread, "content", "v1")
	if err == nil {
		t.Fatal("GetSummary: want error on a corrupt stored record")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Errorf("error kind = %v (ok=%v), want integrity", k, ok)
	}
}

// TestSummarizerGetSummaryStoragePutFailure: a Store.Put failure during
// regeneration degrades like any other regeneration failure, and its Reason
// names the STORE rather than the dispatch -- the distinction an operator
// needs to know whether the model or the disk is the problem.
func TestSummarizerGetSummaryStoragePutFailure(t *testing.T) {
	exec := &fakeExecutor{output: "x"}
	store := &flakyStore{MemStore: storetest.NewMemStore(), putErr: cascade.New(cascade.KindUnavailable, "disk full")}
	s := newTestSummarizer(t, exec, store)

	outcome, err := s.GetSummary(context.Background(), "entity-1", GranularityThread, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: want nil error on a degrade-gracefully path, got %v", err)
	}
	if outcome.Failed == nil {
		t.Fatal("outcome.Failed = nil, want a SummarizationFailedEvent when persistence fails")
	}
	for _, want := range []string{"writing summary to storage", "disk full"} {
		if !strings.Contains(outcome.Failed.Reason, want) {
			t.Errorf("Failed.Reason = %q, want substring %q", outcome.Failed.Reason, want)
		}
	}
	if strings.Contains(outcome.Failed.Reason, "dispatch") {
		t.Errorf("Failed.Reason = %q names the dispatch, but the dispatch succeeded", outcome.Failed.Reason)
	}
}

// TestSummarizeRefusesFailedOutcome: with no summary on record and a failing
// dispatch, Summarize returns an error. Compose treats that exactly like "no
// summarizer configured" and trims the slot, which is the fail-closed path;
// returning empty content instead would look to Compose like a successful,
// tiny substitute.
func TestSummarizeRefusesFailedOutcome(t *testing.T) {
	exec := &fakeExecutor{err: cascade.New(cascade.KindUnavailable, "model unreachable")}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())

	got, err := s.Summarize(context.Background(), Slot{Label: "entity-z", Content: "c"}, 1000)
	if err == nil {
		t.Fatal("Summarize: want error when no fresh summary is available")
	}
	if got.Content != "" {
		t.Errorf("Summarize returned content %q alongside its error, want none", got.Content)
	}
	if !strings.Contains(err.Error(), "no fresh summary") {
		t.Errorf("error = %q, want it to say no fresh summary was available", err.Error())
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Errorf("error kind = %v (ok=%v), want unavailable", k, ok)
	}
}

// TestSummarizeRefusesStaleOutcome: a STALE summary is the dangerous case --
// it is real text describing a previous version of the source, so serving it
// would put content that contradicts the assembly into the assembly.
// Summarize must refuse it even though a perfectly readable summary exists.
func TestSummarizeRefusesStaleOutcome(t *testing.T) {
	exec := &fakeExecutor{output: "the summary of the first version"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	ctx := context.Background()

	first, err := s.Summarize(ctx, Slot{Label: "entity-s", Content: "the original content"}, 1000)
	if err != nil {
		t.Fatalf("seeding Summarize: %v", err)
	}
	if first.Content != "the summary of the first version" {
		t.Fatalf("seeding Summarize returned %q, want the generated summary", first.Content)
	}

	exec.err = cascade.New(cascade.KindUnavailable, "model unreachable")
	got, err := s.Summarize(ctx, Slot{Label: "entity-s", Content: "entirely different content"}, 1000)
	if err == nil {
		t.Fatal("Summarize: want an error rather than the stale summary")
	}
	if got.Content != "" {
		t.Errorf("Summarize returned stale content %q, want none", got.Content)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("error = %q, want it to say the summary was stale", err.Error())
	}
}

// TestSummarizerPublishesDegradedEvents asserts both non-fatal events reach
// the injected publisher -- otherwise a summarizer that has stopped working
// degrades silently, which is the failure mode the events exist to prevent.
func TestSummarizerPublishesDegradedEvents(t *testing.T) {
	exec := &fakeExecutor{output: "the first summary"}
	pub := &recordingPublisher{}
	s := newTestSummarizerWithEvents(t, exec, storetest.NewMemStore(), pub)
	ctx := context.Background()

	if _, err := s.GetSummary(ctx, "entity-p", GranularityThread, "content v1", "v1"); err != nil {
		t.Fatalf("seeding GetSummary: %v", err)
	}
	if got := len(pub.all()); got != 0 {
		t.Fatalf("%d event(s) published for a successful regeneration, want 0", got)
	}

	exec.err = cascade.New(cascade.KindUnavailable, "model unreachable")
	if _, err := s.GetSummary(ctx, "entity-p", GranularityThread, "content v2", "v2"); err != nil {
		t.Fatalf("second GetSummary: %v", err)
	}
	events := pub.all()
	if len(events) != 2 {
		t.Fatalf("published %d event(s) for a stale-plus-failed degrade, want 2: %+v", len(events), events)
	}
	if events[0].Failed == nil || events[0].Stale != nil {
		t.Errorf("events[0] = %+v, want only a Failed event", events[0])
	}
	if events[1].Stale == nil || events[1].Failed != nil {
		t.Errorf("events[1] = %+v, want only a Stale event", events[1])
	}
	if events[1].Stale.Level != GranularityThread || events[1].Stale.EntityID != "entity-p" {
		t.Errorf("stale event = %+v, want it to name entity-p at the thread level", events[1].Stale)
	}
}
