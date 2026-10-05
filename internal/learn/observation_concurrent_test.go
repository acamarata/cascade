package learn

// Purpose: Observe under real concurrency. A pool with several connections
//   (the production shape) runs many writers on one cell; every observation
//   must land. The shared test helper pins one connection, so it cannot see
//   a lost or failed concurrent write; this file opens its own pool.
// SPORT: internal.learn.ObservationWriter/TESTED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
)

const (
	concurrentWriters  = 8
	concurrentPerWrite = 20
)

// openPoolDB copies the migrated template and opens it with an 8-connection
// pool and a 60 s busy timeout. A non-empty pragma (journal_mode) is applied
// once, on a single connection, before the pool opens: journal_mode is
// persistent in the file, and re-applying it on every fresh pool connection
// is itself a lock-taking step that returns SQLITE_BUSY on windows while
// another connection holds the write lock.
func openPoolDB(t *testing.T, pragma string) *sql.DB {
	t.Helper()
	src, err := templatePath(tmplOutcome)
	if err != nil {
		t.Fatalf("build migrated template: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	path := filepath.Join(t.TempDir(), "pool.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy template: %v", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(60000)"
	if pragma != "" {
		setupDB, err := sql.Open("sqlite", dsn+pragma)
		if err != nil {
			t.Fatalf("open setup db: %v", err)
		}
		if err := setupDB.Ping(); err != nil {
			t.Fatalf("apply %s: %v", pragma, err)
		}
		_ = setupDB.Close()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open pool db: %v", err)
	}
	db.SetMaxOpenConns(concurrentWriters)
	t.Cleanup(func() { _ = db.Close() })
	if pragma != "" {
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode on the pool = %q, %v; want wal persisted from setup", mode, err)
		}
	}
	return db
}

// TestObserveConcurrent: 8 goroutines x 20 Observe on ONE cell over a pool of
// 8 connections, in the default journal mode and in WAL. Every call returns
// nil and the stored observation_count equals the attempts, with alpha+beta
// equal to the same count (no decay: the clock is fixed).
func TestObserveConcurrent(t *testing.T) {
	for name, pragma := range map[string]string{"journal": "", "wal": "&_pragma=journal_mode(WAL)"} {
		t.Run(name, func(t *testing.T) {
			db := openPoolDB(t, pragma)
			s := NewSQLiteCapabilityScorer(db, newTestClock())
			scope, tc, tier := repoScope("repo-concurrent"), conductor.TaskClassCode, capacity.TierOne
			var wg sync.WaitGroup
			errs := make(chan error, concurrentWriters*concurrentPerWrite)
			for w := 0; w < concurrentWriters; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; i < concurrentPerWrite; i++ {
						errs <- s.Observe(context.Background(), scope, tc, tier, (w+i)%2 == 0)
					}
				}(w)
			}
			wg.Wait()
			close(errs)
			failed := 0
			for err := range errs {
				if err != nil {
					failed++
					if failed == 1 {
						t.Errorf("first Observe error: %v", err)
					}
				}
			}
			row, ok, err := readRow(context.Background(), db, scope, tc, tier)
			want := concurrentWriters * concurrentPerWrite
			if err != nil || !ok || failed != 0 || row.count != want || !near(row.alpha+row.beta, float64(want)) {
				t.Errorf("failed = %d, stored = %+v (found %v, err %v), want 0 failures and count %d with alpha+beta %d",
					failed, row, ok, err, want, want)
			}
		})
	}
}
