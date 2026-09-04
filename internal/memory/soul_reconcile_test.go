package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestReconcileNoOpWhenTheFileAgrees proves the quiet case is quiet: no
// version moves, no entry is appended, no event is emitted and no note is
// left when the file says what the store says.
func TestReconcileNoOpWhenTheFileAgrees(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "settled")

	res, err := f.store.DetectDivergence(ctx)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.Outcome != DivergenceNone {
		t.Fatalf("outcome = %q, want %q", res.Outcome, DivergenceNone)
	}
	if res.Version != 1 {
		t.Fatalf("version = %d, want 1", res.Version)
	}
	if len(f.sink.events) != 0 {
		t.Fatalf("a no-op emitted %d events", len(f.sink.events))
	}
	if _, err := os.Stat(f.notePath()); !os.IsNotExist(err) {
		t.Fatalf("a no-op left a diagnostic note: %v", err)
	}
	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(export.AuditEntries) != 1 {
		t.Fatalf("a no-op appended an audit entry: %d", len(export.AuditEntries))
	}
}

// TestReconcileAdoptsACleanExternalEdit proves route (b)'s middle case:
// the user edited the file in their own editor, and that edit becomes a
// recorded, versioned change rather than an untracked one.
func TestReconcileAdoptsACleanExternalEdit(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "written through the store")
	if _, err := f.store.Get(ctx); err != nil {
		t.Fatalf("confirming read: %v", err)
	}

	writeFileAs(t, f.documentPath(), "written in my own editor")
	res, err := f.store.DetectDivergence(ctx)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.Outcome != DivergenceReconciled {
		t.Fatalf("outcome = %q, want %q", res.Outcome, DivergenceReconciled)
	}
	if res.Version != 2 || res.LastReconciledVersion != 2 {
		t.Fatalf("version/reconciled = %d/%d, want 2/2", res.Version, res.LastReconciledVersion)
	}
	if len(f.sink.events) != 0 {
		t.Fatal("a clean external edit was reported as a conflict")
	}
	view, err := f.store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Document.Body != "written in my own editor" || view.Diverged {
		t.Fatalf("adopted view is wrong: %+v", view)
	}
	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := export.AuditEntries[1].Route; got != SoulRouteFileReconcile {
		t.Fatalf("route = %q, want %q", got, SoulRouteFileReconcile)
	}
	// A second reconcile over an unchanged file must not adopt it again.
	if res2, err := f.store.DetectDivergence(ctx); err != nil || res2.Outcome != DivergenceNone {
		t.Fatalf("repeat reconcile = %+v, %v", res2, err)
	}
}

// divergedFixture drives a store into the conflict state: a store-side
// write that has not been seen on disk, followed by an out-of-store edit.
func divergedFixture(t *testing.T) soulFixture {
	t.Helper()
	f := newSoulFixture(t)
	f.mustEdit(t, "the store's version")
	writeFileAs(t, f.documentPath(), "the editor's version")
	return f
}

// TestReconcileConflictTouchesNothing is the heart of route (b): when both
// sides have moved, the store adopts neither, merges nothing, and discards
// nothing. It says so loudly — on the bus and in a note — and refuses.
func TestReconcileConflictTouchesNothing(t *testing.T) {
	ctx := context.Background()
	f := divergedFixture(t)

	res, err := f.store.DetectDivergence(ctx)
	if !errors.Is(err, ErrSoulDiverged) {
		t.Fatalf("detect = %v, want ErrSoulDiverged", err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindConflict {
		t.Fatalf("kind = %v, want conflict", kind)
	}
	if res.Outcome != DivergenceConflict {
		t.Fatalf("outcome = %q, want %q", res.Outcome, DivergenceConflict)
	}
	// Neither side was applied.
	onDisk, err := os.ReadFile(f.documentPath())
	if err != nil || string(onDisk) != "the editor's version" {
		t.Fatalf("the file was rewritten: %q, %v", onDisk, err)
	}
	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(export.AuditEntries) != 1 {
		t.Fatalf("a conflict appended an audit entry: %d", len(export.AuditEntries))
	}
	if export.Soul.Body != "the editor's version" {
		t.Fatalf("export shows %q; a conflicted read must still show the file", export.Soul.Body)
	}
	view, err := f.store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !view.Diverged {
		t.Fatal("a conflicted read did not tell the caller the document may be stale")
	}
}

// TestReconcileConflictEventAndNoteCarryNoSoulText is the second canary.
//
// A conflict fans out to every bus subscriber and leaves a note that gets
// pasted into bug reports. Both must be able to say WHAT happened without
// carrying one byte of what either side of the document says.
func TestReconcileConflictEventAndNoteCarryNoSoulText(t *testing.T) {
	f := newSoulFixture(t)
	const storeCanary = "CANARY-STORE-SIDE-7b3a"
	const fileCanary = "CANARY-EDITOR-SIDE-1c9d"

	f.mustEdit(t, "the store said "+storeCanary)
	writeFileAs(t, f.documentPath(), "the editor said "+fileCanary)
	if _, err := f.store.DetectDivergence(context.Background()); !errors.Is(err, ErrSoulDiverged) {
		t.Fatalf("detect = %v, want ErrSoulDiverged", err)
	}
	if len(f.sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(f.sink.events))
	}
	ev := f.sink.events[0]
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	note, err := os.ReadFile(f.notePath())
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	for _, needle := range []string{storeCanary, fileCanary, "the store said", "the editor said"} {
		if strings.Contains(string(payload), needle) {
			t.Fatalf("the event leaked %q: %s", needle, payload)
		}
		if strings.Contains(string(note), needle) {
			t.Fatalf("the note leaked %q: %s", needle, note)
		}
	}
	// It still says enough to act on: which versions, which digests, and
	// what to do next.
	if ev.StoredHash == "" || ev.FileHash == "" || ev.StoredHash == ev.FileHash {
		t.Fatalf("the event does not identify the two sides: %+v", ev)
	}
	if ev.DetectedAt.IsZero() {
		t.Fatal("the event has no instant")
	}
	if !strings.Contains(string(note), soulNoteRemediation) {
		t.Fatalf("the note offers no remediation: %s", note)
	}
	// And it names no machine path, which is the other thing a pasted
	// diagnostic must not carry.
	if strings.Contains(string(note), f.base) || strings.Contains(string(note), soulDocumentFile) {
		t.Fatalf("the note names a machine path: %s", note)
	}
}

// TestReconcileMissingDocumentIsAConflictNotAnAdoptedDeletion proves an
// identity document deleted out from under the store is never silently
// accepted (leaving a version and a log for a document that is gone) and
// never silently undone by rewriting the file from the store.
func TestReconcileMissingDocumentIsAConflictNotAnAdoptedDeletion(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "I am Ada.")
	if err := os.Remove(f.documentPath()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	res, err := f.store.DetectDivergence(ctx)
	if !errors.Is(err, ErrSoulDiverged) {
		t.Fatalf("detect = %v, want ErrSoulDiverged", err)
	}
	if res.Outcome != DivergenceConflict || res.FileHash != "" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(f.documentPath()); !os.IsNotExist(err) {
		t.Fatalf("the store recreated a document the user deleted: %v", err)
	}
	if _, err := f.store.Get(ctx); !errors.Is(err, ErrNoSoulDocument) {
		t.Fatalf("Get() = %v, want ErrNoSoulDocument", err)
	}
	// Before the first write, an absent file is simply an absent file.
	fresh := newSoulFixture(t)
	res, err = fresh.store.DetectDivergence(ctx)
	if err != nil || res.Outcome != DivergenceNone {
		t.Fatalf("fresh store = %+v, %v", res, err)
	}
	if len(fresh.sink.events) != 0 {
		t.Fatal("an unwritten soul emitted a divergence event")
	}
}
