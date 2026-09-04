package memory

// Purpose: the consolidation job's failure paths — what an interruption
//   leaves behind, what a damaged file does to a run, and what a failing
//   event sink does not do.
// Constraints: every store lives under t.TempDir(); the file-system seam
//   is the unexported test double, never a shipped alternative.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestConsolidationInterruptedLeavesEitherOriginalOrCompleteResult is the
// crash-contract test.
//
// It interrupts the run at the very first write — the consolidation record
// itself — and asserts that the tree is byte-for-byte what it was: every
// record still live, no tombstone, no half-written account. The record is
// written before any member is retired precisely so that an interruption
// before it lands is indistinguishable from a run that never happened.
func TestConsolidationInterruptedLeavesEitherOriginalOrCompleteResult(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	seedPair(t, store)
	before := treeSnapshot(t, base)

	broken := newConsolidatorWithFS(base, store, clk, nil, newCountingFS(1))
	report, err := broken.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err == nil {
		t.Fatal("a failing file system produced no error")
	}
	if report.Merged != 0 {
		t.Errorf("report claims %d merged group(s) after a failed write", report.Merged)
	}
	assertTreeUnchanged(t, before, treeSnapshot(t, base))
}

// TestConsolidationResumesAfterAnInterruptedRun proves the other half of
// the contract: when the account IS on disk but the retirements did not
// all land, the next run finishes the job rather than being confused by
// the partial state.
func TestConsolidationResumesAfterAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	f.write(t, "third", "same\n", "c", KindProject)

	// Retire one member out of band, exactly as an interrupted run would
	// have left it, and write no account for it.
	if err := f.store.Delete(ctx, KindProject, "second"); err != nil {
		t.Fatalf("simulating a partial retirement: %v", err)
	}

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}
	if report.Merged != 1 || report.Retired != 1 {
		t.Fatalf("report = %+v, want the remaining duplicate retired", report)
	}
	rec := f.readConsolidation(t, KindProject, "first")
	if len(rec.Members) != 1 || rec.Members[0].ID != "project/third" {
		t.Errorf("the account names %+v, want only the record this run retired", rec.Members)
	}
}

// TestConsolidationAccountAccumulates proves a later run does not
// overwrite an earlier account. A survivor that absorbs a second duplicate
// months later must still be able to explain the first one.
func TestConsolidationAccountAccumulates(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	if _, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	f.write(t, "third", "same\n", "c", KindProject)
	if _, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	rec := f.readConsolidation(t, KindProject, "first")
	ids := make([]string, 0, len(rec.Members))
	for _, m := range rec.Members {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "project/second,project/third" {
		t.Fatalf("the account names %v; an earlier retirement was overwritten", ids)
	}
}

// TestConsolidationDamagedRecordIsReportedNotMerged proves one bad file
// neither fails the run nor gets retired.
func TestConsolidationDamagedRecordIsReportedNotMerged(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	damaged := filepath.Join(f.base, string(KindProject), "broken.md")
	if err := os.WriteFile(damaged, []byte("not a memory record"), 0o600); err != nil {
		t.Fatalf("seeding a damaged file: %v", err)
	}

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("one damaged file failed the whole run: %v", err)
	}
	if report.Merged != 1 {
		t.Errorf("report = %+v, want the healthy duplicates still merged", report)
	}
	if len(report.Unreadable) != 1 || report.Unreadable[0] != "project/broken" {
		t.Errorf("Unreadable = %v, want the damaged record named", report.Unreadable)
	}
	if _, statErr := os.Stat(damaged); statErr != nil {
		t.Errorf("the damaged record was removed: %v", statErr)
	}
}

// TestConsolidationSinkFailureIsNotFatal proves a bus that refuses the
// event does not undo, or fail, a merge that is already durable.
func TestConsolidationSinkFailureIsNotFatal(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.sink.failWith = errors.New("the bus is down")
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)

	report, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if err != nil {
		t.Fatalf("a sink failure failed the job: %v", err)
	}
	if report.Merged != 1 || report.Retired != 1 {
		t.Fatalf("report = %+v, want the merge reported despite the sink failure", report)
	}
	if _, err := f.store.Read(ctx, KindProject, "second"); !errors.Is(err, ErrNoSuchEntry) {
		t.Errorf("the retirement did not persist: %v", err)
	}
}

// TestConsolidationDamagedAccountIsNotOverwritten proves the run refuses
// rather than replacing an account it cannot read — the account of records
// already removed is the last thing that may be destroyed.
func TestConsolidationDamagedAccountIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	f := newConsolidationFixture(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	path := filepath.Join(f.base, consolidationsDir, string(KindProject), "first"+consolidationSuffix)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("preparing the account directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("seeding a damaged account: %v", err)
	}

	_, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{})
	if !errors.Is(err, ErrMalformedConsolidation) {
		t.Fatalf("err = %v, want ErrMalformedConsolidation", err)
	}
	if _, err := f.store.Read(ctx, KindProject, "second"); err != nil {
		t.Errorf("a record was retired despite the unreadable account: %v", err)
	}
}

// TestConsolidationReadFailureIsReturned proves a file-system fault on the
// read path stops the run rather than being mistaken for an empty store.
func TestConsolidationReadFailureIsReturned(t *testing.T) {
	base := t.TempDir()
	clk := newTestClock()
	broken := newFailingFS()
	broken.failListDir = true
	store := newFileStoreWithFS(base, clk, broken)
	c := newConsolidatorWithFS(base, store, clk, nil, broken)

	if _, err := c.ConsolidateMemories(context.Background(), ConsolidationConfig{}); err == nil {
		t.Fatal("an unreadable store produced no error")
	}
}

// TestConsolidationCanceledContextIsRefused proves the job does not start
// work on a context that is already done.
func TestConsolidationCanceledContextIsRefused(t *testing.T) {
	f := newConsolidationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.c.ConsolidateMemories(ctx, ConsolidationConfig{}); err == nil {
		t.Fatal("a canceled context produced no error")
	}
}

// TestConsolidationSkipsWhileAnotherRunIsInFlight proves the in-process
// guard: a manual trigger arriving during a scheduled run stands down with
// a report saying so, rather than racing it or blocking on it.
func TestConsolidationSkipsWhileAnotherRunIsInFlight(t *testing.T) {
	f := newConsolidationFixture(t)
	f.c.running.Lock()
	defer f.c.running.Unlock()

	report, err := f.c.ConsolidateMemories(context.Background(), ConsolidationConfig{})
	if err != nil {
		t.Fatalf("a skipped run returned an error: %v", err)
	}
	if !report.Skipped || report.Merged != 0 {
		t.Fatalf("report = %+v, want Skipped with nothing merged", report)
	}
}

// seedPair writes two byte-identical records through store.
func seedPair(t *testing.T, store *FileStore) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{"first", "second"} {
		e := validEntry()
		e.Name, e.Body, e.Kind = name, "same\n", KindProject
		if err := store.Write(ctx, e); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
}
