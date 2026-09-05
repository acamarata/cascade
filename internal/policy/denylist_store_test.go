// Purpose: the B-layer-backed deny-list over a REAL store — providers/
// sqlite on a t.TempDir() file (Art.2, Art.7.1), never an in-memory double
// — covering the Add/Remove/List round trip, persistence across a
// simulated daemon restart, cache invalidation, the class-scoped query,
// and every fail-closed direction a stored row can fail in.
//
// SPORT: internal/policy StoreDenyList/ADDED (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/providers/sqlite"
)

// denyFixture is a real database file plus the engine over it.
type denyFixture struct {
	path   string
	db     *sqlite.Driver
	engine *StoreDenyList
}

// newDenyFixture opens a real SQLite database in a temp dir.
func newDenyFixture(t *testing.T) *denyFixture {
	t.Helper()
	f := &denyFixture{path: filepath.Join(t.TempDir(), "cascade.db")}
	f.open(t)
	return f
}

// open (re)opens the database and rebuilds the engine over it, dropping
// the in-memory cache exactly as a daemon restart would.
func (f *denyFixture) open(t *testing.T) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	f.db = db
	engine, err := NewStoreDenyList(db)
	if err != nil {
		t.Fatalf("NewStoreDenyList: %v", err)
	}
	f.engine = engine
	t.Cleanup(func() { _ = db.Close() })
}

// reopen closes and reopens the database, simulating a daemon restart.
func (f *denyFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	f.open(t)
}

// TestDenyListRoundTripsThroughTheStore covers Add, List, Denied and
// Remove against a real file, including the cache invalidation each write
// must perform and the persistence a restart must preserve.
func TestDenyListRoundTripsThroughTheStore(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)

	// Reading before anything is written primes the cache with an empty
	// list, so a stale cache after Add would show up as a miss below.
	if denied, err := f.engine.Denied(ctx, "rm -rf /srv"); denied || err != nil {
		t.Fatalf("an empty deny-list matched: %v, %v", denied, err)
	}
	if err := f.engine.Add(ctx, "rm -rf *", ClassDestructivePrivileged); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.engine.Add(ctx, "curl https://example.test/x", anyActionClass); err != nil {
		t.Fatalf("Add exact: %v", err)
	}
	for _, action := range []string{"rm -rf /srv", "curl https://example.test/x"} {
		denied, err := f.engine.Denied(ctx, action)
		if err != nil || !denied {
			t.Fatalf("Denied(%q) = %v, %v; want a match after Add", action, denied, err)
		}
	}
	list, err := f.engine.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v; want the two written rows", list, err)
	}
	if list[0].Pattern != "curl https://example.test/x" || list[1].Pattern != "rm -rf *" {
		t.Fatalf("List is not in pattern order: %v", list)
	}

	f.reopen(t)
	if denied, err := f.engine.Denied(ctx, "rm -rf /srv"); err != nil || !denied {
		t.Fatalf("the row did not survive a restart: %v, %v", denied, err)
	}

	if err := f.engine.Remove(ctx, "rm -rf *"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if denied, err := f.engine.Denied(ctx, "rm -rf /srv"); err != nil || denied {
		t.Fatalf("a removed pattern still matches (%v, %v): the cache was not invalidated",
			denied, err)
	}
	if err := f.engine.Remove(ctx, "never-added"); err != nil {
		t.Errorf("removing an absent pattern returned %v", err)
	}
}

// TestDenyListRefusesAnInvalidPatternAtWrite asserts a pattern the matcher
// could not honour never reaches storage.
func TestDenyListRefusesAnInvalidPatternAtWrite(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	for _, bad := range []string{"", "  ", "rm [ab]", `rm \x`} {
		if err := f.engine.Add(ctx, bad, anyActionClass); !errors.Is(err, ErrDenyListPatternInvalid) {
			t.Errorf("Add(%q) = %v, want %s", bad, err, CodeDenyListPatternInvalid)
		}
	}
	if err := f.engine.Add(ctx, "rm *", ActionClass(200)); !errors.Is(err, ErrDenyListPatternInvalid) {
		t.Errorf("Add with an out-of-range class = %v, want a refusal", err)
	}
	list, err := f.engine.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("a refused pattern reached storage: %v, %v", list, err)
	}
}

// TestDenyListFailsClosedOnACorruptRow is hard requirement 2 at the store
// boundary: a row that will not decode, a row whose stored pattern no
// longer satisfies the grammar, and a row filed under another pattern's
// key each REFUSE the whole read. None of them is skipped, because a
// listing that quietly omitted a deny-list row would hide the entry the
// operator is relying on.
func TestDenyListFailsClosedOnACorruptRow(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		key   string
		value []byte
	}{
		{"an undecodable row", denyKey("rm *"), []byte("not json")},
		{"a row whose pattern is empty", denyKey(""), []byte(`{"pattern":""}`)},
		{"a row whose pattern has a class", denyKey("rm [ab]"), []byte(`{"pattern":"rm [ab]"}`)},
		{"a row filed under another key", denyKey("ls"), []byte(`{"pattern":"rm *"}`)},
		{"a row with an unknown class", denyKey("rm *"), []byte(`{"pattern":"rm *","class":200}`)},
	}
	for _, tc := range cases {
		f := newDenyFixture(t)
		if err := f.db.Put(ctx, string(storage.DomainPolicy), tc.key, tc.value); err != nil {
			t.Fatalf("%s: seeding: %v", tc.name, err)
		}
		denied, err := f.engine.Denied(ctx, "ls")
		if err == nil {
			t.Errorf("%s: Denied returned (%v, nil); a corrupt row must refuse", tc.name, denied)
		}
		if denied {
			t.Errorf("%s: a refusal also reported a match", tc.name)
		}
		if _, err := f.engine.List(ctx); err == nil {
			t.Errorf("%s: List silently skipped the corrupt row", tc.name)
		}
	}
}

// TestDenyListEngine_ContainsClass covers the class-keyed query shape
// (R-21.4) over the same rows Denied matches.
func TestDenyListEngine_ContainsClass(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "rm -rf *", ClassDestructivePrivileged); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := f.engine.ContainsClass(ctx, ClassDestructivePrivileged, "rm -rf /srv")
	if err != nil || !got {
		t.Fatalf("ContainsClass on the listed pair = %v, %v; want true", got, err)
	}
	got, err = f.engine.ContainsClass(ctx, ClassDestructivePrivileged, "ls -la")
	if err != nil || got {
		t.Fatalf("ContainsClass on an unlisted action = %v, %v; want false", got, err)
	}
	if _, err := f.engine.ContainsClass(ctx, ClassDestructivePrivileged, "${x"); err == nil {
		t.Error("ContainsClass allowed an action it could not normalize")
	}
}

// TestContainsClass_ClassScopedRowMatches is R-21.233: a scoped row
// answers for its own class and not another, and a wildcard row answers
// for every class. Denied ignores the scoping entirely, because it asks
// about a command and not about a class.
func TestContainsClass_ClassScopedRowMatches(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "git push*", ClassExternalSideEffect); err != nil {
		t.Fatalf("Add scoped: %v", err)
	}
	if err := f.engine.Add(ctx, "touch *", anyActionClass); err != nil {
		t.Fatalf("Add wildcard: %v", err)
	}
	cases := []struct {
		name   string
		class  ActionClass
		action string
		want   bool
	}{
		{"a scoped row answers for its own class", ClassExternalSideEffect, "git push", true},
		{"a scoped row is silent for another class", ClassWorkspaceMutation, "git push", false},
		{"a scoped row is silent for the top class", ClassDestructivePrivileged, "git push", false},
		{"a wildcard row answers for a low class", ClassRead, "touch a.txt", true},
		{"a wildcard row answers for the top class", ClassDestructivePrivileged, "touch a.txt", true},
		{"an unlisted action is never listed", ClassExternalSideEffect, "ls", false},
	}
	for _, tc := range cases {
		got, err := f.engine.ContainsClass(ctx, tc.class, tc.action)
		if err != nil {
			t.Errorf("%s: ContainsClass: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: ContainsClass(%s, %q) = %v, want %v",
				tc.name, tc.class, tc.action, got, tc.want)
		}
	}
	// Denied is the other query shape over the same rows: it matches both
	// rows regardless of the class they are scoped to.
	for _, action := range []string{"git push", "touch a.txt"} {
		denied, err := f.engine.Denied(ctx, action)
		if err != nil || !denied {
			t.Errorf("Denied(%q) = %v, %v; want a match", action, denied, err)
		}
	}
}

// TestDenyListWriteRefusesAClosedStore asserts a write that cannot land is
// reported as a refusal rather than swallowed, so a caller never believes
// it stored an entry it did not.
func TestDenyListWriteRefusesAClosedStore(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	if err := f.engine.Add(ctx, "rm *", anyActionClass); !errors.Is(err, ErrDenyListStoreError) {
		t.Errorf("Add against a closed store = %v, want %s", err, CodeDenyListStoreError)
	}
	if err := f.engine.Remove(ctx, "rm *"); !errors.Is(err, ErrDenyListStoreError) {
		t.Errorf("Remove against a closed store = %v, want %s", err, CodeDenyListStoreError)
	}
	if _, err := f.engine.List(ctx); !errors.Is(err, ErrDenyListStoreError) {
		t.Errorf("List against a closed store = %v, want %s", err, CodeDenyListStoreError)
	}
	if denied, err := f.engine.Denied(ctx, "ls"); denied || err == nil {
		t.Errorf("Denied against a closed store = %v, %v; want a refusal", denied, err)
	}
}
