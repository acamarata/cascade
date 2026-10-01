// Purpose: the fail-closed probe this HIGH-risk change owes (persisted,
//   versioned formats are never below the HIGH bar): an input that
//   PASSES persistence.go's decodeRecord control flow on the loose check
//   alone (its first four bytes are recordMagic) but is truncated short of
//   a full header must be refused with a hard error, never silently
//   reinterpreted as a legacy body-only record and migrated into an
//   ordinary deliverable message. Before persistence.go's decodeRecord fix
//   (this change), `len(buf) < recordHeaderSize || !bytes.Equal(...)` used
//   OR: a short-but-magic-prefixed buffer failed the length half of that
//   check and fell through to migrateLegacy, which reads recordMagic's own
//   bytes as a bogus big-endian attempt count and whatever remained as
//   payload — corruption silently accepted and delivered as real work.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestQueue_TruncatedModernRecordFailsClosed is the fail-closed
// probe: a record whose first four bytes are recordMagic — the ONLY signal
// decodeRecord uses to claim a buffer as its own format — but which is
// truncated short of the fixed header must make Dequeue fail loudly, and
// must never rewrite or otherwise touch the corrupted bytes in Store.
func TestQueue_TruncatedModernRecordFailsClosed(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()

	// "QRC1" (recordMagic) + 6 more bytes: passes the magic check, but at
	// 10 bytes it is far short of recordHeaderSize (19) — truncated mid
	// header, not a foreign or legacy buffer.
	corruptID := "00000000000000000001-eeeeeeee"
	corrupt := []byte("QRC1\x00\x00\x00\x00\x00")
	requireNoErr(t, store.Put(ctx, "ns", "msg:"+corruptID, corrupt), "seed truncated modern-format record")

	q := queue.New(store, clock, queue.Config{})
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	if err == nil {
		t.Fatalf("Dequeue over a truncated modern-format record = %+v, nil error, want a refusal — corruption must never be silently accepted as a deliverable message", msg)
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Dequeue over a truncated modern-format record: want KindIntegrity, got %v", err)
	}
	if msg != nil {
		t.Fatalf("Dequeue over a truncated modern-format record returned a message %+v, want nil alongside the error", msg)
	}

	cur, getErr := store.Get(ctx, "ns", "msg:"+corruptID)
	requireNoErr(t, getErr, "Get after refused truncated record")
	if string(cur) != string(corrupt) {
		t.Fatalf("corrupted record was rewritten by the refused recovery attempt: before %x, after %x", corrupt, cur)
	}
}
