package conversation

// Purpose: write-once privacy markers (privacy_createthread.go) over a real
//   sqlite store: an equal tier is a no-op, a different tier is refused with
//   the stored marker unchanged, concurrent writers end on exactly one
//   winner, and a thread that already holds turns without a marker keeps
//   its implicit restricted tier.
// SPORT: internal.conversation.privacy (write-once markers, tests).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// markerRows counts threadID's rows in the marker table directly, so a test
// can tell "no marker" from "a marker that reads restricted".
func markerRows(t *testing.T, store Store, threadID string) int {
	t.Helper()
	var n int
	db := store.(*conversationStore).db
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+tableThreadPrivacy+` WHERE thread_id = ?`, threadID).Scan(&n); err != nil {
		t.Fatalf("count marker rows: %v", err)
	}
	return n
}

// isSentinel matches a cascade sentinel by identity AND message: errors.Is
// alone compares the Kind only, so it would accept any error of that kind.
func isSentinel(err, want error) bool {
	return errors.Is(err, want) && err.Error() == want.Error()
}

// TestSetThreadPrivacyWriteOnce: a marker is written once. The same tier
// again is a no-op; a different tier is KindConflict and the stored marker
// (still exactly one row) is unchanged. Failing input: an upsert (ON
// CONFLICT DO UPDATE) lets the second, different write replace the first.
func TestSetThreadPrivacyWriteOnce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SetThreadPrivacy(ctx, "th-x", provider.SensitivityPublic); err != nil {
		t.Fatalf("first SetThreadPrivacy: %v", err)
	}
	if err := store.SetThreadPrivacy(ctx, "th-x", provider.SensitivityPublic); err != nil {
		t.Fatalf("same-tier SetThreadPrivacy = %v, want nil (write-once is idempotent for an equal tier)", err)
	}
	err := store.SetThreadPrivacy(ctx, "th-x", provider.SensitivityLocalOnly)
	if !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "privacy markers are write-once") {
		t.Fatalf("different-tier SetThreadPrivacy = %v, want the write-once KindConflict", err)
	}
	got, err := store.ThreadPrivacy(ctx, "th-x")
	if err != nil || got != provider.SensitivityPublic {
		t.Fatalf("ThreadPrivacy = %s, %v; want public, the first write", got, err)
	}
	if n := markerRows(t, store, "th-x"); n != 1 {
		t.Fatalf("marker rows = %d, want exactly 1", n)
	}
}

// openSharedFileStore opens a Store over a sqlite FILE with several
// connections and a busy timeout, so concurrent writers really overlap and
// wait for the write lock instead of failing with SQLITE_BUSY.
func openSharedFileStore(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "privacy-race.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)&_synchronous=OFF")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return NewStore(db)
}

// TestSetThreadPrivacyWriteOnce_ConcurrentWriters releases writers of four
// different tiers together, over a multi-connection file database, on one
// thread id. From the STORED state: exactly one marker row exists, its tier
// is what ThreadPrivacy reports, and every writer either got nil with the
// stored tier or the write-once conflict naming the stored tier. Failing
// input: an upsert (ON CONFLICT DO UPDATE) lets later writers replace the
// marker, so a writer that got nil no longer matches the stored tier.
func TestSetThreadPrivacyWriteOnce_ConcurrentWriters(t *testing.T) {
	store := openSharedFileStore(t)
	tiers := []provider.SensitivityTier{provider.SensitivityLocalOnly, provider.SensitivityPublic,
		provider.SensitivityInternal, provider.SensitivityRestricted}
	const writers = 16
	errs := make([]error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = store.SetThreadPrivacy(context.Background(), "th-race", tiers[i%len(tiers)])
		}(i)
	}
	close(start)
	wg.Wait()
	stored, err := store.ThreadPrivacy(context.Background(), "th-race")
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}
	if n := markerRows(t, store, "th-race"); n != 1 {
		t.Fatalf("marker rows = %d, want exactly 1", n)
	}
	nils := 0
	for i, err := range errs {
		tier := tiers[i%len(tiers)]
		switch {
		case err == nil:
			nils++
			if tier != stored {
				t.Fatalf("writer %d (%s) got nil but the stored tier is %s", i, tier, stored)
			}
		case !isSentinel(err, errThreadPrivacyAlreadySet("th-race", stored.String(), tier)):
			t.Fatalf("writer %d (%s): %v, want nil or the write-once conflict against %s", i, tier, err, stored)
		}
	}
	if nils != writers/len(tiers) {
		t.Fatalf("%d writers succeeded, want exactly the %d that wrote the stored tier %s", nils, writers/len(tiers), stored)
	}
}

// TestSetThreadPrivacyRefusesUnmarkedThreadWithTurns: a thread that already
// holds a turn and no marker reads restricted, and that tier is never
// re-labelled: every tier is refused with errUnmarkedThreadHasTurns and no
// marker row appears. Failing input: dropping the turn check lets a
// widening "public" land on existing content.
func TestSetThreadPrivacyRefusesUnmarkedThreadWithTurns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedThread(t, store, "th-old")
	if turns, err := store.ListTurns(ctx, "th-old"); err != nil || len(turns) != 1 {
		t.Fatalf("seeded turns = %d, %v; want 1", len(turns), err)
	}
	for _, tier := range []provider.SensitivityTier{provider.SensitivityPublic, provider.SensitivityInternal,
		provider.SensitivityRestricted, provider.SensitivityLocalOnly} {
		err := store.SetThreadPrivacy(ctx, "th-old", tier)
		if !isSentinel(err, errUnmarkedThreadHasTurns) {
			t.Fatalf("SetThreadPrivacy(%s) on an unmarked thread with turns = %v, want %v", tier, err, errUnmarkedThreadHasTurns)
		}
	}
	if n := markerRows(t, store, "th-old"); n != 0 {
		t.Fatalf("marker rows = %d, want 0 (the refused insert must roll back)", n)
	}
	if got, err := store.ThreadPrivacy(ctx, "th-old"); err != nil || got != provider.SensitivityRestricted {
		t.Fatalf("ThreadPrivacy = %s, %v; want restricted, never widened", got, err)
	}
	// A marked thread with turns is unaffected: its equal tier still answers nil.
	if err := store.SetThreadPrivacy(ctx, "th-new", provider.SensitivityInternal); err != nil {
		t.Fatalf("mark th-new: %v", err)
	}
	seedThread(t, store, "th-new")
	if err := store.SetThreadPrivacy(ctx, "th-new", provider.SensitivityInternal); err != nil {
		t.Fatalf("equal tier on a marked thread with turns = %v, want nil", err)
	}
}
