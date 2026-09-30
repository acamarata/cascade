//go:build postgres

// Purpose: prove CAS races against a real PostgreSQL server, including zero-row refusal.
// Inputs: CASCADE_TEST_POSTGRES_DSN; an isolated local test database.
// Outputs: sixteen-writer create/swap races and shared backend conformance.
// Constraints: skip explicitly without a server; hold writes until every contender waits.
// SPORT: providers.postgres.Store/CHANGED (P1-SEC-37).
package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/postgres"
)

func openCASStore(t *testing.T) (*postgres.Driver, *sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set: concurrent CAS requires a real PostgreSQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	d, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Log(version)
	return d, db, ctx
}

func TestPostgresConcurrentCreateOneWins(t *testing.T) { postgresCASRace(t, nil) }
func TestPostgresConcurrentSwapOneWins(t *testing.T)   { postgresCASRace(t, []byte("old")) }

func TestPostgresConcurrentCASConformance(t *testing.T) {
	storetest.RunConcurrentCAS(t, func(t *testing.T) provider.Store {
		d, _, _ := openCASStore(t)
		return d
	})
}

type postgresCASResult struct {
	value []byte
	err   error
}

func postgresCASRace(t *testing.T, old []byte) {
	t.Helper()
	d, db, ctx := openCASStore(t)
	namespace := t.Name()
	if err := d.Delete(ctx, namespace, "cas"); err != nil {
		t.Fatal(err)
	}
	if old != nil {
		if err := d.Put(ctx, namespace, "cas", old); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback() }()
	if _, err := lock.ExecContext(ctx, "LOCK TABLE kv IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	results := make(chan postgresCASResult, 16)
	for i := range 16 {
		go func() {
			value := []byte(fmt.Sprintf("writer-%d", i))
			err := d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
				return tx.CompareAndSwap(ctx, namespace, "cas", old, value)
			})
			results <- postgresCASResult{value: value, err: err}
		}()
	}
	waitCASWriters(ctx, t, db)
	if err := lock.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCASWinner(ctx, t, d, namespace, results)
}

// waitCASWriters makes the old SELECT-then-upsert read the same state in
// every transaction before any write can proceed. No timing sleep picks a winner.
func waitCASWriters(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks
   WHERE relation = 'kv'::regclass AND mode = 'RowExclusiveLock' AND NOT granted`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 16 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("only %d/16 CAS writers reached the lock: %v", waiting, ctx.Err())
		}
	}
}

func assertCASWinner(ctx context.Context, t *testing.T, d provider.Store, namespace string, results <-chan postgresCASResult) {
	t.Helper()
	winners := 0
	var winner []byte
	for range 16 {
		select {
		case result := <-results:
			if result.err == nil {
				winners++
				winner = result.value
			} else if !cascade.HasKind(result.err, cascade.KindConflict) {
				t.Errorf("losing CAS = %v, want KindConflict", result.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want exactly 1 of 16", winners)
	}
	got, err := d.Get(ctx, namespace, "cas")
	if err != nil || !bytes.Equal(got, winner) {
		t.Fatalf("stored = %q, err = %v; want winner %q", got, err, winner)
	}
}

func TestPostgresCASZeroRowsConflict(t *testing.T) {
	d, _, ctx := openCASStore(t)
	ns := t.Name()
	for _, tc := range []struct {
		name   string
		key    string
		seeded bool
		old    []byte
	}{
		{"create-existing", "create-existing", true, nil},
		{"swap-stale", "swap-stale", true, []byte("stale")},
		{"swap-missing", "swap-missing", false, []byte("old")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.seeded {
				if err := d.Put(ctx, ns, tc.key, []byte("current")); err != nil {
					t.Fatal(err)
				}
			} else if err := d.Delete(ctx, ns, tc.key); err != nil {
				t.Fatal(err)
			}
			err := d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
				return tx.CompareAndSwap(ctx, ns, tc.key, tc.old, []byte("replacement"))
			})
			if !cascade.HasKind(err, cascade.KindConflict) {
				t.Fatalf("CAS = %v, want KindConflict", err)
			}
			got, err := d.Get(ctx, ns, tc.key)
			if !tc.seeded {
				if !cascade.HasKind(err, cascade.KindNotFound) {
					t.Fatalf("missing CAS created value: %q, %v", got, err)
				}
			} else if err != nil || string(got) != "current" {
				t.Fatalf("conflict changed state: %q, %v", got, err)
			}
		})
	}
}

// TestConcurrentCASRejectsDoubleSuccess is a negative control for the shared
// harness: a child test must identify exactly two reported successes.
func TestConcurrentCASRejectsDoubleSuccess(t *testing.T) {
	if os.Getenv("CASCADE_CAS_DOUBLE_SUCCESS") == "1" {
		storetest.RunConcurrentCAS(t, func(t *testing.T) provider.Store {
			t.Helper()
			return &doubleSuccessStore{Store: storetest.NewMemStore()}
		})
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestConcurrentCASRejectsDoubleSuccess$", "-test.v")
	cmd.Env = append(os.Environ(), "CASCADE_CAS_DOUBLE_SUCCESS=1")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "CAS winners = 2, want exactly 1 of 16") {
		t.Fatalf("shared CAS harness did not reject two successes: err=%v\n%s", err, output)
	}
}

// doubleSuccessStore reports one extra successful transaction without committing
// it, testing that the harness counts successes independently of stored bytes.
type doubleSuccessStore struct {
	provider.Store
	mu        sync.Mutex
	successes int
}

func (s *doubleSuccessStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.Store.Tx(ctx, fn)
	if err == nil {
		s.successes++
	} else if cascade.HasKind(err, cascade.KindConflict) && s.successes < 2 {
		s.successes++
		return nil
	}
	return err
}
