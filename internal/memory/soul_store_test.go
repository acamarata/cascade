package memory

// Purpose: the store's central promises — every route is audited, the
//   version is monotonic across routes, the delta hash identifies a
//   transition without revealing it, every field round-trips through a
//   fresh store, and an interrupted write loses nothing.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSoulEveryRouteIsAudited proves the central promise: whichever of the
// three sanctioned routes a change arrives by, the version increases by
// exactly one and exactly one audit entry is appended naming that route.
// The version is monotonic ACROSS routes, not per route.
func TestSoulEveryRouteIsAudited(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)

	// (a) the CLI verb.
	view := f.mustEdit(t, "one")
	if view.Version != 1 {
		t.Fatalf("cli edit produced version %d, want 1", view.Version)
	}
	// (c) the chat-mediated API, a real method with a real implementation.
	if err := f.store.EditViaChat(ctx, SoulDocument{Body: "two"}); err != nil {
		t.Fatalf("edit via chat: %v", err)
	}
	// (b) an edit made in the user's own editor, adopted on load. The
	// reconcile that precedes it is the one the daemon runs at start and
	// `soul show` runs on every read; without it the store cannot tell an
	// editor that saw version 2 from one that did not, and says so.
	if _, err := f.store.Get(ctx); err != nil {
		t.Fatalf("reconcile before the external edit: %v", err)
	}
	writeFileAs(t, f.documentPath(), "three")
	if _, err := f.store.DetectDivergence(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	want := []SoulEditRoute{SoulRouteCLI, SoulRouteChat, SoulRouteFileReconcile}
	if len(export.AuditEntries) != len(want) {
		t.Fatalf("audit log has %d entries, want %d", len(export.AuditEntries), len(want))
	}
	for i, route := range want {
		e := export.AuditEntries[i]
		if e.Version != i+1 {
			t.Fatalf("entry %d has version %d, want %d", i, e.Version, i+1)
		}
		if e.Route != route {
			t.Fatalf("entry %d has route %q, want %q", i, e.Route, route)
		}
		if e.DeltaHash == "" {
			t.Fatalf("entry %d has no delta hash", i)
		}
	}
	if export.Soul.Body != "three" {
		t.Fatalf("body = %q, want %q", export.Soul.Body, "three")
	}
}

// TestSoulDeltaHashIdentifiesTheTransition proves the audit log is
// verifiable without being readable: the same transition always hashes the
// same way, a different one hashes differently, and neither hash is the
// digest of the body alone (which would let a guessed body be confirmed
// against the log).
func TestSoulDeltaHashIdentifiesTheTransition(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "first")
	f.mustEdit(t, "second")

	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	first, second := export.AuditEntries[0], export.AuditEntries[1]
	if first.DeltaHash == second.DeltaHash {
		t.Fatal("two different transitions produced the same delta hash")
	}
	if first.DeltaHash != HashBody(":"+HashBody("first")) {
		t.Fatalf("delta hash is not the transition digest: %q", first.DeltaHash)
	}
	if second.DeltaHash == HashBody("second") {
		t.Fatal("delta hash is the body digest, which would confirm a guessed body")
	}
}

// TestSoulGetBeforeFirstWrite proves an unwritten SOUL is reported as
// absent rather than as an empty document. An empty document would be read
// by every consumer as "nothing is known about this person", which is a
// wrong model rather than a missing one.
func TestSoulGetBeforeFirstWrite(t *testing.T) {
	_, err := newSoulFixture(t).store.Get(context.Background())
	if !errors.Is(err, ErrNoSoulDocument) {
		t.Fatalf("Get() = %v, want ErrNoSoulDocument", err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound {
		t.Fatalf("kind = %v, want not-found", kind)
	}
}

// TestSoulRoundTripsEveryField writes every field the store persists,
// reads it back through a SECOND store over the same directory (so nothing
// in memory can carry the answer), and compares field by field.
func TestSoulRoundTripsEveryField(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	body := "I am Ada.\n\n- I like precision\n- unicode: ê ✓ 漢\n- trailing space  \n"
	if _, err := f.store.Edit(ctx, SoulDocument{Body: body, Schema: "mine/v7"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	f.clock.Advance(90 * time.Second)
	if err := f.store.EditViaChat(ctx, SoulDocument{Body: body + "more\n", Schema: "mine/v7"}); err != nil {
		t.Fatalf("edit via chat: %v", err)
	}

	reopened := NewFileSoulStore(f.base, f.clock, nil)
	view, err := reopened.Get(ctx)
	if err != nil {
		t.Fatalf("get from a fresh store: %v", err)
	}
	if view.Document.Body != body+"more\n" {
		t.Fatalf("body did not round-trip: %q", view.Document.Body)
	}
	if view.Document.Schema != "mine/v7" {
		t.Fatalf("schema did not round-trip: %q", view.Document.Schema)
	}
	if view.Version != 2 {
		t.Fatalf("version = %d, want 2", view.Version)
	}
	if view.Diverged {
		t.Fatal("a store that wrote its own file reports divergence")
	}
	export, err := reopened.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(export.AuditEntries) != 2 {
		t.Fatalf("audit log lost entries across a reopen: %d", len(export.AuditEntries))
	}
	if !export.AuditEntries[1].EditedAt.After(export.AuditEntries[0].EditedAt) {
		t.Fatal("audit timestamps did not round-trip in order")
	}
	if export.AuditEntries[0].EditedAt.Location() != time.UTC {
		t.Fatal("audit timestamp did not round-trip in UTC")
	}
}

// TestSoulInterruptedWriteLosesNothing proves what the write ordering buys.
//
// The document lands before the ledger, so an interruption between them
// leaves the real content on disk and a ledger that has not claimed it.
// The next read must therefore do three things and no others: keep the
// content that is on disk, refuse to report a version for content the
// ledger never recorded, and say plainly that the two sides disagree. What
// it must never do is present a half-erased or stale document as the
// current, authoritative model of the user.
func TestSoulInterruptedWriteLosesNothing(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "committed")
	before, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	// The document write lands; the ledger write does not.
	writeFileAs(t, f.documentPath(), "interrupted write")
	writeFileAs(t, f.ledgerPath(), string(before))

	view, err := f.store.Get(ctx)
	if err != nil {
		t.Fatalf("get after an interrupted write: %v", err)
	}
	if view.Document.Body != "interrupted write" {
		t.Fatalf("interrupted content lost: %q", view.Document.Body)
	}
	if !view.Diverged {
		t.Fatal("an unclaimed document was presented as the current soul")
	}
	if _, err := os.Stat(f.notePath()); err != nil {
		t.Fatalf("no diagnostic note was left: %v", err)
	}
	// A person resolves it by saying what the document should be. That
	// write is recorded like any other and restores agreement.
	if _, err := f.store.Edit(ctx, SoulDocument{Body: "interrupted write"}); err != nil {
		t.Fatalf("resolving edit: %v", err)
	}
	after, err := f.store.Get(ctx)
	if err != nil {
		t.Fatalf("get after resolution: %v", err)
	}
	if after.Diverged || after.Version != 2 {
		t.Fatalf("resolution left the store diverged: %+v", after)
	}
}
