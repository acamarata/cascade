package economics

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestReserveOverrideTTLAndCap(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 123, time.UTC))
	store := newTestStore(t, clock)
	for _, ttl := range []time.Duration{-time.Second, 0, 4*time.Hour + time.Nanosecond} {
		if err := store.AllowReserve(ctx, "project", ttl); err != ErrReserveOverrideTTL || err.Error() != "invalid-input: economics: reserve override ttl must be positive and at most 4h" {
			t.Fatalf("ttl %v: %v", ttl, err)
		}
	}
	assertReserveAllowed(t, store, "project", false)
	if err := store.AllowReserve(ctx, "project", 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	assertReserveAllowed(t, store, "project", true)
	clock.Advance(4*time.Hour - time.Nanosecond)
	assertReserveAllowed(t, store, "project", true)
	clock.Advance(time.Nanosecond)
	assertReserveAllowed(t, store, "project", false)
	clock.Advance(time.Nanosecond)
	assertReserveAllowed(t, store, "project", false)
	checkReserveCap(t, store)
	clock.Advance(7 * 24 * time.Hour)
	if err := store.AllowReserve(ctx, "project", time.Hour); err != nil {
		t.Fatalf("week rollover: %v", err)
	}
	t.Run("replacement", testReserveReplacement)
}

func checkReserveCap(t *testing.T, store *SchedulerModeStore) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := store.AllowReserve(ctx, "project", time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := store.ClearReserve(ctx, "project"); err != nil {
			t.Fatal(err)
		}
		assertReserveAllowed(t, store, "project", false)
	}
	if err := store.AllowReserve(ctx, "project", time.Hour); err != ErrReserveOverrideCap || err.Error() != "conflict: economics: reserve override cap reached for this project this week" {
		t.Fatalf("fourth grant: %v", err)
	}
	assertReserveAllowed(t, store, "project", false)
	entries, err := store.IncidentJournal(ctx, "project")
	if err != nil || len(entries) != 1 || entries[0].Event != EventHumanApprovalRequired || entries[0].ApprovalID != ApprovalIDReserveOverrideCap {
		t.Fatalf("journal = %+v, error = %v", entries, err)
	}
	if err := store.AllowReserve(ctx, "other", time.Hour); err != nil {
		t.Fatalf("separate project: %v", err)
	}
	assertReserveAllowed(t, store, "other", true)
}

func assertReserveAllowed(t *testing.T, store *SchedulerModeStore, project string, want bool) {
	t.Helper()
	got, err := store.ReserveAllowed(context.Background(), project)
	if err != nil || got != want {
		t.Fatalf("ReserveAllowed(%q) = %v, %v; want %v", project, got, err, want)
	}
}

func TestReserveOverrideReadErrorDenies(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()
	if err := store.AllowReserve(ctx, "project", time.Hour); err != nil {
		t.Fatal(err)
	}
	assertReserveAllowed(t, store, "project", true)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	allowed, err := store.ReserveAllowed(ctx, "project")
	if allowed || err == nil {
		t.Fatalf("read failure = %v, %v; want false, error", allowed, err)
	}
	if errors.Is(err, ErrReserveOverrideCap) {
		t.Fatalf("read failure misclassified: %v", err)
	}
}

func TestReserveOverrideMigration(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/mode.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	set := MigrationSet()
	if set.SetID != "fleet-economics-mode" || set.SchemaVersion != 2 || set.ReaderCeiling != 2 {
		t.Fatalf("migration identity/version = %+v", set)
	}
	old := set
	old.SchemaVersion, old.ReaderCeiling, old.Steps = 1, 1, old.Steps[:2]
	if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock}, old); err != nil {
		t.Fatal(err)
	}
	store := NewSchedulerModeStore(db, clock)
	before, err := store.Set(ctx, "project", ModeIncident, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
			t.Fatalf("migration #%d: %v", i+1, err)
		}
	}
	after, err := store.Resolve(ctx, "project", "implement")
	if err != nil || after.ProjectID != before.ProjectID || after.Mode != before.Mode || after.Source != before.Source || !after.SetAt.Equal(before.SetAt) || !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("existing row changed: before=%+v after=%+v err=%v", before, after, err)
	}
	assertReserveAllowed(t, store, "project", false)
	if err := store.AllowReserve(ctx, "project", time.Hour); err != nil {
		t.Fatal(err)
	}
	assertReserveAllowed(t, store, "project", true)
}

func testReserveReplacement(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()
	for _, ttl := range []time.Duration{4 * time.Hour, time.Hour} {
		if err := store.AllowReserve(ctx, "project", ttl); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Hour)
	assertReserveAllowed(t, store, "project", false)
}
