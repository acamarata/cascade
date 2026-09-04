package secrets

// Purpose: the quarantine store's tests. The load-bearing ones are the
//
//	canary (no flagged byte reaches the store file), the reversibility
//	pair (an entry can be released and is then gone from the live set
//	while the ledger still records what happened), and the concurrency
//	test that must be clean under -race.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testStore builds a store in a temp dir on a frozen clock.
func testStore(t *testing.T) *QuarantineStore {
	t.Helper()
	store, err := NewQuarantineStore(t.TempDir(), fixedClock{at: time.Unix(1700000000, 0).UTC()})
	if err != nil {
		t.Fatalf("opening the quarantine store: %v", err)
	}
	return store
}

// classHit builds a hit of the given class for store tests.
func classHit(class Class, offset int) DetectionHit {
	return DetectionHit{
		Class: class, Pattern: "test", Offset: offset, Len: 12,
		Confidence: ConfidenceCertain, SuggestedName: defaultNameFor(class),
	}
}

// allClasses is every class the registry can emit.
var allClasses = []Class{
	ClassAPIKey, ClassJWT, ClassBearer, ClassPEM, ClassConnString, ClassBase64JSON,
}

// TestQuarantineRoundTripsEveryClass covers Put/Get/List/Delete over all
// six credential classes.
func TestQuarantineRoundTripsEveryClass(t *testing.T) {
	store := testStore(t)
	ids := make([]string, 0, len(allClasses))
	for i, class := range allClasses {
		entry, err := store.Put(classHit(class, i*100), "memory:note:1", []byte("value-"+string(class)))
		if err != nil {
			t.Fatalf("putting %s: %v", class, err)
		}
		if entry.ID == "" || entry.Fingerprint == "" {
			t.Fatalf("%s produced an entry with no id or fingerprint: %+v", class, entry)
		}
		ids = append(ids, entry.ID)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(listed) != len(allClasses) {
		t.Fatalf("listed %d entries, want %d", len(listed), len(allClasses))
	}
	for _, id := range ids {
		got, gerr := store.Get(id)
		if gerr != nil || got.ID != id {
			t.Fatalf("get %s: %+v %v", id, got, gerr)
		}
		if derr := store.Delete(id, ReleaseFalsePositive); derr != nil {
			t.Fatalf("delete %s: %v", id, derr)
		}
	}
	count, err := store.PendingCount()
	if err != nil || count != 0 {
		t.Fatalf("after releasing everything: count=%d err=%v", count, err)
	}
}

// TestQuarantineFileHoldsNoValue is the canary: a planted secret must not
// appear anywhere in the persisted bytes, in any encoding.
func TestQuarantineFileHoldsNoValue(t *testing.T) {
	const canary = "sk-CANARYvalue0123456789abcdefghij"
	store := testStore(t)
	if _, err := store.Put(classHit(ClassAPIKey, 7), "memory:note:9", []byte(canary)); err != nil {
		t.Fatalf("putting: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, quarantineLogName))
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{canary, "CANARYvalue", "sk-CANARY"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the quarantine file contains %q: %s", forbidden, text)
		}
	}
}

// TestQuarantineIDIsDeterministic: the same finding recorded twice yields
// the same id, so a re-scan does not multiply entries.
func TestQuarantineIDIsDeterministic(t *testing.T) {
	store := testStore(t)
	first, err := store.Put(classHit(ClassJWT, 3), "doc:a", []byte("same-bytes"))
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, err := store.Put(classHit(ClassJWT, 3), "doc:a", []byte("same-bytes"))
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ for the same finding: %q vs %q", first.ID, second.ID)
	}
	other, err := store.Put(classHit(ClassJWT, 4), "doc:a", []byte("same-bytes"))
	if err != nil {
		t.Fatalf("third put: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("a different offset produced the same id")
	}
}

// TestQuarantineFalsePositiveRecovery is the reversibility contract: a
// wrongly-quarantined entry can be released, disappears from the live
// set, and the release stays in the ledger as an accounted event.
func TestQuarantineFalsePositiveRecovery(t *testing.T) {
	store := testStore(t)
	entry, err := store.Put(classHit(ClassBase64JSON, 1), "doc:b", []byte("value"))
	if err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := store.Delete(entry.ID, ReleaseFalsePositive); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if _, err := store.Get(entry.ID); err == nil {
		t.Fatal("a released entry is still readable")
	}
	if err := store.Delete(entry.ID, ReleaseFalsePositive); err == nil {
		t.Fatal("releasing twice succeeded")
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, quarantineLogName))
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	if !strings.Contains(string(raw), ReleaseFalsePositive) {
		t.Fatal("the ledger does not record why the entry was released")
	}
}

// TestQuarantineErrorPaths covers the refusals.
func TestQuarantineErrorPaths(t *testing.T) {
	store := testStore(t)
	if _, err := store.Put(DetectionHit{Class: ClassAPIKey}, "ref", nil); err == nil {
		t.Error("a hit with no suggested name was accepted")
	}
	if _, err := store.Get("no-such-id"); err == nil {
		t.Error("an unknown id was found")
	}
	if err := store.Delete("no-such-id", ReleasePromoted); err == nil {
		t.Error("an unknown id was released")
	}
	entry, err := store.Put(classHit(ClassPEM, 0), "ref", nil)
	if err != nil {
		t.Fatalf("putting without a value: %v", err)
	}
	if entry.Fingerprint != "" {
		t.Errorf("a nil value produced a fingerprint: %q", entry.Fingerprint)
	}
	if err := store.Delete(entry.ID, ""); err == nil {
		t.Error("a release with no reason was accepted")
	}
	if _, err := NewQuarantineStore("", fixedClock{}); err == nil {
		t.Error("an empty directory was accepted")
	}
	if _, err := NewQuarantineStore(t.TempDir(), nil); err == nil {
		t.Error("a nil clock was accepted")
	}
}

// TestQuarantineSurvivesACorruptLine: one torn line costs one record, not
// the whole ledger. The operator must still see everything else.
func TestQuarantineSurvivesACorruptLine(t *testing.T) {
	store := testStore(t)
	if _, err := store.Put(classHit(ClassAPIKey, 1), "doc:c", []byte("v1")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	path := filepath.Join(store.dir, quarantineLogName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening the ledger: %v", err)
	}
	if _, err := f.WriteString("{not json at all\n{}\n"); err != nil {
		t.Fatalf("writing the torn line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if _, err := store.Put(classHit(ClassJWT, 2), "doc:c", []byte("v2")); err != nil {
		t.Fatalf("putting after the torn line: %v", err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatalf("listing over a torn ledger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("listed %d entries over a torn ledger, want 2: %+v", len(entries), entries)
	}
}

// TestQuarantineMissingLedgerIsEmptyNotAnError: a store that has never
// recorded anything lists nothing rather than failing.
func TestQuarantineMissingLedgerIsEmptyNotAnError(t *testing.T) {
	count, err := testStore(t).PendingCount()
	if err != nil || count != 0 {
		t.Fatalf("a fresh store reported count=%d err=%v", count, err)
	}
}

// TestQuarantineKeyIsReusedAcrossOpens: reopening the same directory must
// keep ids stable, or a doctor check would see every entry change id on
// every daemon restart.
func TestQuarantineKeyIsReusedAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	clock := fixedClock{at: time.Unix(1700000000, 0).UTC()}
	first, err := NewQuarantineStore(dir, clock)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	a, err := first.Put(classHit(ClassAPIKey, 5), "doc:d", []byte("bytes"))
	if err != nil {
		t.Fatalf("putting: %v", err)
	}
	second, err := NewQuarantineStore(dir, clock)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	b, err := second.Put(classHit(ClassAPIKey, 5), "doc:d", []byte("bytes"))
	if err != nil {
		t.Fatalf("putting after reopen: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("the id changed across reopen: %q vs %q", a.ID, b.ID)
	}
	info, err := os.Stat(filepath.Join(dir, quarantineKeyName))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the quarantine key is mode %o, want 600", perm)
	}
}

// TestQuarantineConcurrentPut must be clean under -race and must lose no
// record: the store is written from every scan boundary at once.
func TestQuarantineConcurrentPut(t *testing.T) {
	store := testStore(t)
	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(n int) {
			defer wg.Done()
			if _, err := store.Put(classHit(ClassAPIKey, n), "doc:e", []byte{byte(n)}); err != nil {
				t.Errorf("concurrent put %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	count, err := store.PendingCount()
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != writers {
		t.Fatalf("counted %d entries after %d concurrent puts", count, writers)
	}
}
