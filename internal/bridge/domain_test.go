// Purpose: internal/bridge's tests — the emitted schema, the round trip, the
//
//	restart-survival property the whole package exists for, and the
//	integrity refusals on a corrupted list column.
//
// Constraints: a live in-memory SQLite database per test, matching
//
//	internal/ci/domain_test.go's identical helper. No CASCADE_HOME, no file
//	on disk, no network.
//
// SPORT: internal.bridge.Store/TESTED, internal.bridge.MigrationSet/TESTED
//
//	(P1-E23-W5-S48-T1).
package bridge

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock { return fakeClock{t: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)} }

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migratedStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{},
		newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return NewStore(db), db
}

func TestMigrationSet_Shape(t *testing.T) {
	set := MigrationSet()
	if set.SetID != "bridge" {
		t.Fatalf("SetID = %q, want \"bridge\" (R-16.77 per-set ledger identity)", set.SetID)
	}
	if set.SchemaVersion != bridgeSchemaVersion || set.ReaderCeiling != bridgeSchemaVersion {
		t.Fatalf("version/ceiling = %d/%d, want %d", set.SchemaVersion, set.ReaderCeiling, bridgeSchemaVersion)
	}
	ddl, err := migrate.SQLiteEmitter{}.Emit(set)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	joined := strings.Join(ddl, "\n")
	for _, want := range []string{tableSubject, "allowed_from", "code_digest", "wrong_attempts",
		"poll_offset", "seen_update_ids"} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted DDL is missing %q", want)
		}
	}
}

func TestApplyMigrationSchema_IsIdempotent(t *testing.T) {
	_, db := migratedStore(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema: %v", err)
	}
	var name string
	row := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableSubject)
	if err := row.Scan(&name); err != nil {
		t.Fatalf("table %s missing after migration: %v", tableSubject, err)
	}
}

func TestApplyMigrationSchema_RefusesNilArguments(t *testing.T) {
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("a nil db was accepted")
	}
	db := openTestDB(t)
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("a nil clock was accepted")
	}
}

func TestStore_LoadMissingSubjectIsNotAnError(t *testing.T) {
	store, _ := migratedStore(t)
	row, ok, err := store.Load(context.Background(), "tg-nothing")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok {
		t.Fatalf("Load reported a row for an unknown subject: %+v", row)
	}
}

// TestStore_RoundTripsEveryField is the durability proof: everything the bridge
// persists comes back, with times surviving the millisecond encoding and the
// zero time staying zero rather than becoming 1970.
func TestStore_RoundTripsEveryField(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	want := SubjectRow{
		Subject:       "tg-abc1234567890",
		TrustTier:     "paired-device",
		PairedAt:      time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
		AllowedFrom:   []string{"111", "222"},
		CodeDigest:    strings.Repeat("a", 64),
		CodeExpiresAt: time.Date(2026, 9, 20, 10, 10, 0, 0, time.UTC),
		WrongAttempts: 3,
		Offset:        900000123,
		SeenUpdateIDs: []int64{900000121, 900000122},
	}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := store.Load(ctx, want.Subject)
	if err != nil || !ok {
		t.Fatalf("Load = (%v, %v)", ok, err)
	}
	if got.Subject != want.Subject || got.TrustTier != want.TrustTier ||
		!got.PairedAt.Equal(want.PairedAt) || !got.CodeExpiresAt.Equal(want.CodeExpiresAt) ||
		got.CodeDigest != want.CodeDigest || got.WrongAttempts != want.WrongAttempts ||
		got.Offset != want.Offset {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	if strings.Join(got.AllowedFrom, ",") != "111,222" {
		t.Fatalf("AllowedFrom = %v", got.AllowedFrom)
	}
	if len(got.SeenUpdateIDs) != 2 || got.SeenUpdateIDs[0] != 900000121 {
		t.Fatalf("SeenUpdateIDs = %v", got.SeenUpdateIDs)
	}
}

func TestStore_ZeroTimesStayZero(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, SubjectRow{Subject: "tg-zero"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := store.Load(ctx, "tg-zero")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.PairedAt.IsZero() || !got.CodeExpiresAt.IsZero() {
		t.Fatalf("zero times came back as %v / %v", got.PairedAt, got.CodeExpiresAt)
	}
	if len(got.AllowedFrom) != 0 || len(got.SeenUpdateIDs) != 0 {
		t.Fatalf("nil lists came back as %v / %v", got.AllowedFrom, got.SeenUpdateIDs)
	}
}

// TestStore_SaveUpserts: a second Save replaces the row rather than failing on
// the primary key, which is what a read-modify-write cycle needs — but only
// when it presents the version its own Load returned (see cas.go and
// cas_test.go; a second Save of a STALE copy is refused, deliberately).
func TestStore_SaveUpserts(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	first := SubjectRow{Subject: "tg-upsert", AllowedFrom: []string{"111"}, Offset: 1}
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, _, err := store.Load(ctx, "tg-upsert")
	if err != nil {
		t.Fatalf("Load between writes: %v", err)
	}
	second.AllowedFrom = append(second.AllowedFrom, "222")
	second.Offset = 2
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, _, err := store.Load(ctx, "tg-upsert")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Offset != 2 || len(got.AllowedFrom) != 2 {
		t.Fatalf("upsert produced %+v", got)
	}
}

func TestStore_RefusesAnEmptySubject(t *testing.T) {
	store, _ := migratedStore(t)
	if err := store.Save(context.Background(), SubjectRow{}); err == nil {
		t.Fatal("a row with no subject was written")
	}
}

// TestStore_CorruptedListColumnIsAnIntegrityFailure is the load-bearing half of
// "the decode is total": a corrupted allowlist must NOT read back as an empty
// one, because an empty allowlist means "unpaired" and would re-open a bound
// bridge to every sender.
func TestStore_CorruptedListColumnIsAnIntegrityFailure(t *testing.T) {
	store, db := migratedStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, SubjectRow{Subject: "tg-corrupt", AllowedFrom: []string{"111"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, column := range []string{"allowed_from", "seen_update_ids"} {
		if _, err := db.ExecContext(ctx,
			`UPDATE `+tableSubject+` SET `+column+` = 'not json' WHERE subject = ?`, "tg-corrupt"); err != nil {
			t.Fatalf("corrupting %s: %v", column, err)
		}
		_, ok, err := store.Load(ctx, "tg-corrupt")
		if err == nil {
			t.Fatalf("a corrupted %s column loaded without an error (ok=%v)", column, ok)
		}
		if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("corrupted %s produced %v, want KindIntegrity", column, err)
		}
		// Restore for the next column.
		if _, rerr := db.ExecContext(ctx,
			`UPDATE `+tableSubject+` SET `+column+` = '[]' WHERE subject = ?`, "tg-corrupt"); rerr != nil {
			t.Fatalf("restoring %s: %v", column, rerr)
		}
	}
}

func TestStore_NilReceiverAndNilDBRefuse(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	if _, _, err := nilStore.Load(ctx, "x"); err == nil {
		t.Fatal("Load on a nil store succeeded")
	}
	if err := nilStore.Save(ctx, SubjectRow{Subject: "x"}); err == nil {
		t.Fatal("Save on a nil store succeeded")
	}
	empty := NewStore(nil)
	if _, _, err := empty.Load(ctx, "x"); err == nil {
		t.Fatal("Load with no db succeeded")
	}
	if err := empty.Save(ctx, SubjectRow{Subject: "x"}); err == nil {
		t.Fatal("Save with no db succeeded")
	}
}

func TestStore_ReadFailurePropagates(t *testing.T) {
	store, db := migratedStore(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := store.Load(context.Background(), "tg-anything"); err == nil {
		t.Fatal("Load against a closed database succeeded")
	}
	if err := store.Save(context.Background(), SubjectRow{Subject: "tg-anything"}); err == nil {
		t.Fatal("Save against a closed database succeeded")
	}
}

func TestMillisRoundTrip(t *testing.T) {
	if millis(time.Time{}) != 0 {
		t.Fatal("the zero time did not encode as 0")
	}
	if !fromMillis(0).IsZero() {
		t.Fatal("0 did not decode as the zero time")
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	if got := fromMillis(millis(at)); !got.Equal(at) {
		t.Fatalf("round trip gave %v, want %v", got, at)
	}
}

func TestEncodeList_NilBecomesAnEmptyArray(t *testing.T) {
	got, err := encodeList[string](nil)
	if err != nil {
		t.Fatalf("encodeList: %v", err)
	}
	if got != "[]" {
		t.Fatalf("encodeList(nil) = %q, want \"[]\"", got)
	}
}

func TestSchemaVersionIsExported(t *testing.T) {
	if SchemaVersion != bridgeSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", SchemaVersion, bridgeSchemaVersion)
	}
}
