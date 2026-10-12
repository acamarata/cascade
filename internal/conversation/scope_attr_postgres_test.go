//go:build postgres

package conversation

// Purpose: the postgres leg of TestThreadScopeMigrationRoundTrip: the
//   thread_scope set applies through the Postgres emitter, a repeat apply is
//   a ledger no-op, and a thread written before it reads unattributed.
// Inputs: CASCADE_TEST_POSTGRES_DSN, a real reachable PostgreSQL server.
// Outputs: test verdicts; skips loudly without a DSN, never passes silently.
// Constraints: everything runs in a throwaway schema dropped at cleanup.
// SPORT: internal.conversation.adapter (scoped reads, tests).

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func init() { postgresScopeLeg = runPostgresScopeLeg }

func runPostgresScopeLeg(t *testing.T) {
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set: the postgres scope leg needs a real PostgreSQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // search_path below is per connection
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("scope_leg_%d", newTestClock().Now().UnixNano())
	for _, stmt := range []string{`CREATE SCHEMA ` + schema, `SET search_path TO ` + schema} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.PostgresEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, MigrationSet()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO conversation_thread (id, name, created_at) VALUES ($1, $1, 1)`, "pre-migration"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := migrate.Apply(ctx, cfg, scopeMigrationSet()); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	var rows, version int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableThreadScope+` WHERE thread_id = $1`, "pre-migration").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("pre-migration thread attributed: %d rows, %v", rows, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations WHERE set_id = $1`, "conversation-thread-scope").Scan(&version); err != nil || version != 1 {
		t.Fatalf("ledger version %d: %v", version, err)
	}
}
