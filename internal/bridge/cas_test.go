package bridge

// Purpose (this file): the compare-and-swap property, driven against a REAL
//
//	sqlite database — the one thing that stops a bridge's read-modify-write
//	from reverting another's columns (and, around a binding, from silently
//	unpairing a bound bot).
//
// Constraints: no CASCADE_HOME, no file on disk, no network; the same
//
//	in-memory database helper the rest of this package's tests use.
//
// SPORT: internal.bridge.Store/TESTED (compare-and-swap) — P1-E23-W5-S48-T1.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSave_StaleWriteIsRefusedAndChangesNothing is the regression the whole
// version column exists for: with the CAS removed (an unconditional upsert),
// the stale writer below wins and the reader that advanced the offset loses it
// silently.
func TestSave_StaleWriteIsRefusedAndChangesNothing(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()

	if err := store.Save(ctx, SubjectRow{Subject: "tg-cas", TrustTier: "paired-device",
		AllowedFrom: []string{"111"}}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Two readers load the SAME version. This is the interleaving the poll
	// goroutine and pa.pair_code really produce in one daemon process.
	first, _, err := store.Load(ctx, "tg-cas")
	if err != nil {
		t.Fatalf("Load(first): %v", err)
	}
	second, _, err := store.Load(ctx, "tg-cas")
	if err != nil {
		t.Fatalf("Load(second): %v", err)
	}

	first.Offset, first.SeenUpdateIDs = 42, []int64{41}
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}

	// The second writer's copy predates that write. It carried an empty
	// AllowedFrom in an earlier draft of the bridge, which is exactly how a
	// bound bot became "unpaired".
	second.AllowedFrom = nil
	err = store.Save(ctx, second)
	if err == nil {
		t.Fatal("a stale write was ACCEPTED; the compare-and-swap is not in force")
	}
	if !IsVersionConflict(err) {
		t.Fatalf("stale Save returned %v, want the typed version conflict", err)
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("stale Save error kind is not conflict: %v", err)
	}

	got, ok, err := store.Load(ctx, "tg-cas")
	if err != nil || !ok {
		t.Fatalf("Load after the refusal = (%v, %v)", ok, err)
	}
	if got.Offset != 42 || len(got.SeenUpdateIDs) != 1 {
		t.Fatalf("the winning write was reverted: offset=%d seen=%v", got.Offset, got.SeenUpdateIDs)
	}
	if len(got.AllowedFrom) != 1 || got.AllowedFrom[0] != "111" {
		t.Fatalf("the allowlist was wiped by a refused write: %v", got.AllowedFrom)
	}
}

// TestSave_VersionAdvancesOnEveryWrite: the token Load hands back is the one
// the NEXT write has to present, so a caller that re-reads can always proceed.
func TestSave_VersionAdvancesOnEveryWrite(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, SubjectRow{Subject: "tg-v"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	row, _, err := store.Load(ctx, "tg-v")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("first version = %d, want 1", row.Version)
	}
	row.Offset = 7
	if err := store.Save(ctx, row); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	again, _, err := store.Load(ctx, "tg-v")
	if err != nil {
		t.Fatalf("Load again: %v", err)
	}
	if again.Version != 2 || again.Offset != 7 {
		t.Fatalf("after the second write: version=%d offset=%d, want 2/7", again.Version, again.Offset)
	}
	// And the FIRST version is no longer writable.
	if err := store.Save(ctx, row); !IsVersionConflict(err) {
		t.Fatalf("re-presenting version %d gave %v, want a conflict", row.Version, err)
	}
}

// TestSave_InsertRefusesARowThatAppearedSince: version 0 means "my Load found
// nothing". If a row exists by the time the write lands, this caller's state is
// stale in the same way, and the write must not clobber it.
func TestSave_InsertRefusesARowThatAppearedSince(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, SubjectRow{Subject: "tg-race", AllowedFrom: []string{"999"}}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := store.Save(ctx, SubjectRow{Subject: "tg-race"})
	if !IsVersionConflict(err) {
		t.Fatalf("a second version-0 insert gave %v, want a conflict", err)
	}
	got, _, _ := store.Load(ctx, "tg-race")
	if len(got.AllowedFrom) != 1 {
		t.Fatalf("the existing allowlist was overwritten: %v", got.AllowedFrom)
	}
}

// TestIsVersionConflict_DoesNotMatchEveryConflict: pkg/cascade's Is compares
// the KIND only, so a predicate that checked the kind alone would call every
// unrelated KindConflict in the tree a CAS miss and retry it.
func TestIsVersionConflict_DoesNotMatchEveryConflict(t *testing.T) {
	if IsVersionConflict(nil) {
		t.Fatal("nil is a conflict")
	}
	other := cascade.New(cascade.KindConflict, "nodes: this device is already enrolled")
	if IsVersionConflict(other) {
		t.Fatal("an unrelated KindConflict was read as a compare-and-swap refusal")
	}
	if !IsVersionConflict(ErrVersionConflict) {
		t.Fatal("the package's own sentinel is not recognised")
	}
}

// TestSave_RefusalsBeforeAnyIO keeps the guards falsifiable: a store with no
// database and a row with no subject are both refused, and neither refusal is a
// version conflict a caller would retry.
func TestSave_RefusalsBeforeAnyIO(t *testing.T) {
	ctx := context.Background()
	var empty *Store
	if err := empty.Save(ctx, SubjectRow{Subject: "tg-x"}); err == nil || IsVersionConflict(err) {
		t.Fatalf("nil store Save = %v", err)
	}
	store, _ := migratedStore(t)
	err := store.Save(ctx, SubjectRow{})
	if err == nil || !strings.Contains(err.Error(), "needs a subject id") {
		t.Fatalf("Save with no subject = %v", err)
	}
}

// TestMigrationSet_CarriesTheVersionColumn: the compare-and-swap is only real
// if the column it swaps on is actually emitted.
func TestMigrationSet_CarriesTheVersionColumn(t *testing.T) {
	ddl, err := migrate.SQLiteEmitter{}.Emit(MigrationSet())
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	joined := strings.Join(ddl, "\n")
	if !strings.Contains(joined, "version") {
		t.Fatalf("the emitted DDL has no version column:\n%s", joined)
	}
}

// TestSave_ExecFailureIsReportedNotSwallowed: a write that the database itself
// refuses must not look like a compare-and-swap miss (which a caller RETRIES)
// and must not look like success. Both statements are covered: the insert path
// (version 0) and the update path.
func TestSave_ExecFailureIsReportedNotSwallowed(t *testing.T) {
	store, db := migratedStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, SubjectRow{Subject: "tg-gone"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	row, _, err := store.Load(ctx, "tg-gone")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE `+tableSubject); err != nil {
		t.Fatalf("drop the table: %v", err)
	}
	row.Offset = 3
	err = store.Save(ctx, row)
	if err == nil {
		t.Fatal("an update against a missing table succeeded")
	}
	if IsVersionConflict(err) {
		t.Fatalf("a database failure was reported as a retryable conflict: %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("update failure kind = %v", err)
	}
	if err := store.Save(ctx, SubjectRow{Subject: "tg-new"}); err == nil || IsVersionConflict(err) {
		t.Fatalf("insert against a missing table = %v", err)
	}
}

// TestConflictIfNoRows_FailsClosedOnAnUnreportableResult: a driver that cannot
// say how many rows it touched is treated as a failure, never as a success — the
// alternative is a silent lost update on exactly the drivers least able to
// report one.
func TestConflictIfNoRows_FailsClosedOnAnUnreportableResult(t *testing.T) {
	err := conflictIfNoRows(0, errors.New("test: this driver cannot count rows"))
	if err == nil {
		t.Fatal("an unreportable result was accepted as a successful write")
	}
	if IsVersionConflict(err) {
		t.Fatalf("it was reported as a retryable conflict: %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("kind = %v", err)
	}
	if err := conflictIfNoRows(1, nil); err != nil {
		t.Fatalf("a single affected row was refused: %v", err)
	}
}
