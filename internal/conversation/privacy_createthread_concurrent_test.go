package conversation

// Purpose: concurrent chat.create_thread mints over a multi-connection file
//   database must all succeed. A deferred transaction that reads before it
//   writes can fail its lock upgrade with SQLITE_BUSY when two mints overlap
//   (the busy timeout does not apply to that deadlock), losing a mint.
// SPORT: internal/conversation (TEST).

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// openSharedMintStore is openSharedFileStore plus the thread-scope table that
// CreateThread's attribution step writes.
func openSharedMintStore(t *testing.T) Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mint-race.db")+"?_pragma=busy_timeout(30000)&_synchronous=OFF")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	clock := newTestClock()
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	if err := ensureScopeSchema(context.Background(), db, clock); err != nil {
		t.Fatalf("ensureScopeSchema: %v", err)
	}
	return NewStore(db)
}

// TestCreateThread_ConcurrentMintsAllSucceed releases many mints of distinct
// ids together; every one must return nil and leave its thread and marker.
func TestCreateThread_ConcurrentMintsAllSucceed(t *testing.T) {
	store := openSharedMintStore(t)
	const mints = 24
	errs := make([]error, mints)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = store.CreateThread(context.Background(), fmt.Sprintf("th-mint-%d", i),
				provider.SensitivityInternal, "attr", 100)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("mint %d: %v, want nil (concurrent mints of distinct ids must not collide)", i, err)
		}
		if n := markerRows(t, store, fmt.Sprintf("th-mint-%d", i)); n != 1 {
			t.Fatalf("mint %d: marker rows = %d, want 1", i, n)
		}
	}
}

// TestCreateThread_ConcurrentSameIDOneWinner releases mints of one id
// together: exactly one wins, the rest report the id already exists.
func TestCreateThread_ConcurrentSameIDOneWinner(t *testing.T) {
	store := openSharedMintStore(t)
	const mints = 16
	errs := make([]error, mints)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = store.CreateThread(context.Background(), "th-same", provider.SensitivityInternal, "attr", 100)
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !isSentinel(err, errThreadExists):
			t.Fatalf("mint %d: %v, want nil or errThreadExists", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d mints won, want exactly 1", wins)
	}
}
