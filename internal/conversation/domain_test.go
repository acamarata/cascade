package conversation

// Purpose: domain.go's own tests -- closed-vocabulary Valid/Decode
//   round-trips, content-addressed id determinism, the no-bare-time.Now
//   lint assertion (A-T2), the migration's real-DB shape, idempotent
//   re-apply, nil-arg refusal, and the downgrade-refusal trigger the
//   acceptance criteria name explicitly.
// SPORT: internal.conversation.domain/ADDED (P1-E20-W5-S43-T1).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests -- no bare time.Now
// (A-T2/Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() migrate.Clock {
	return fakeClock{t: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a REAL modernc-sqlite database file under t.TempDir()
// (Art.2: a real counterpart, never an in-memory self-authored double).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversation-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestStore opens a fresh real db, applies the conversation migration,
// and returns a Store over it.
func newTestStore(t *testing.T) Store {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return NewStore(db)
}

func TestRoleValidAndDecode(t *testing.T) {
	for _, r := range []Role{RoleUser, RoleAssistant, RoleSystem, RoleTool} {
		if !r.Valid() {
			t.Errorf("Role(%q).Valid() = false, want true", r)
		}
		got, err := DecodeRole(string(r))
		if err != nil || got != r {
			t.Errorf("DecodeRole(%q) = %q, %v, want %q, nil", r, got, err, r)
		}
	}
	if Role("").Valid() {
		t.Error("Role(\"\").Valid() = true, want false (fail-closed zero value)")
	}
	if _, err := DecodeRole("bogus"); err == nil {
		t.Error("DecodeRole(\"bogus\") = nil error, want a typed refusal")
	}
	if _, err := DecodeRole(""); err == nil {
		t.Error("DecodeRole(\"\") = nil error, want a typed refusal")
	}
}

func TestSegmentKindValidAndDecode(t *testing.T) {
	for _, k := range []SegmentKind{SegmentText, SegmentCode, SegmentToolCall, SegmentToolResult} {
		if !k.Valid() {
			t.Errorf("SegmentKind(%q).Valid() = false, want true", k)
		}
		got, err := DecodeSegmentKind(string(k))
		if err != nil || got != k {
			t.Errorf("DecodeSegmentKind(%q) = %q, %v, want %q, nil", k, got, err, k)
		}
	}
	if _, err := DecodeSegmentKind("bogus"); err == nil {
		t.Error("DecodeSegmentKind(\"bogus\") = nil error, want a typed refusal")
	}
}

func TestContentAddressedIDsAreDeterministicAndDistinct(t *testing.T) {
	a := NewTurnID("thread-1", 0, RoleUser)
	b := NewTurnID("thread-1", 0, RoleUser)
	if a != b {
		t.Errorf("NewTurnID not deterministic: %q != %q", a, b)
	}
	c := NewTurnID("thread-1", 1, RoleUser)
	if a == c {
		t.Error("NewTurnID(seq=0) == NewTurnID(seq=1), want distinct ids")
	}
	d := NewTurnID("thread-2", 0, RoleUser)
	if a == d {
		t.Error("NewTurnID(thread-1) == NewTurnID(thread-2), want distinct ids")
	}

	sa := NewSegmentID("turn-1", 0, SegmentText)
	sb := NewSegmentID("turn-1", 0, SegmentCode)
	if sa == sb {
		t.Error("NewSegmentID same (turnID, seq) different kind collided")
	}
}

// TestNoBareTimeNowInPackage is this ticket's own A-T2 lint assertion,
// enforced independently of golangci-lint's forbidigo pass: it scans this
// package's own non-test .go source for a bare "time.Now(" call. All
// timestamps in this package come from an injected Clock (domain.go's
// Clock interface / migrate.Clock), never the wall clock read directly.
func TestNoBareTimeNowInPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if strings.Contains(string(body), "time.Now(") {
			t.Errorf("%s calls time.Now() directly; use an injected Clock instead", name)
		}
	}
}

func TestMigrationSetReferenceShape(t *testing.T) {
	stmts, err := migrate.SQLiteEmitter{}.Emit(MigrationSet())
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	all := strings.Join(stmts, "\n")
	for _, want := range []string{
		"conversation_thread", "conversation_turn", "conversation_segment",
		"thread_id", "turn_id", "seq", "role", "kind", "content",
		"idx_conversation_turn_thread_seq", "idx_conversation_segment_turn_seq",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("emitted DDL missing %q; migrations/001_conversation_tables.sql claims this shape", want)
		}
	}
}

func TestApplyConversationSchemaCreatesTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	for _, table := range []string{tableThread, tableTurn, tableSegment} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
}

func TestApplyConversationSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("first ApplyConversationSchema: %v", err)
	}
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyConversationSchema: %v", err)
	}
}

func TestApplyConversationSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Error("ApplyConversationSchema(nil db) = nil, want error")
	}
	db := openTestDB(t)
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("ApplyConversationSchema(nil clock) = nil, want error")
	}
}

// TestConversationMigrationReaderCeilingRefusesDowngrade matches
// internal/providers/registry/migration_test.go's identical test: a
// MigrationSet claiming a lower ReaderCeiling than the ledger's
// on-disk schema_version must be refused.
func TestConversationMigrationReaderCeilingRefusesDowngrade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyConversationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}

	downgraded := MigrationSet()
	downgraded.ReaderCeiling = conversationSchemaVersion - 1
	if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: dialect, Clock: clock}, downgraded); err == nil {
		t.Fatal("Apply with a lowered ReaderCeiling should have been refused, got nil error")
	}
}
