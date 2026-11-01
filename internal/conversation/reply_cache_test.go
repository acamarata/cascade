package conversation

// Purpose: the reply summarizer's in-process store: read, write, delete,
// ordered scan over a snapshot, the size bound, and transaction rollback
// with compare-and-swap.
// Constraints: no database, clock or network.
// SPORT: internal.conversation.reply_store tests (ADD).

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestSummaryCacheGetPutDelete(t *testing.T) {
	ctx := context.Background()
	c := newSummaryCache()
	if _, err := c.Get(ctx, "ns", "k"); !errors.Is(err, errSummaryNotFound) {
		t.Fatalf("absent key: %v, want errSummaryNotFound", err)
	}
	src := []byte("v1")
	if err := c.Put(ctx, "ns", "k", src); err != nil {
		t.Fatal(err)
	}
	src[0] = 'X' // the store must hold its own copy
	got, err := c.Get(ctx, "ns", "k")
	if err != nil || string(got) != "v1" {
		t.Fatalf("Get = %q, %v; want the stored copy v1", got, err)
	}
	got[0] = 'Y' // and hand out copies
	if again, _ := c.Get(ctx, "ns", "k"); string(again) != "v1" {
		t.Fatalf("a returned slice aliased the stored value: %q", again)
	}
	if _, err := c.Get(ctx, "other", "k"); err == nil {
		t.Fatal("a key leaked across namespaces")
	}
	if err := c.Delete(ctx, "ns", "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "ns", "k"); err == nil {
		t.Fatal("a deleted key is still readable")
	}
}

func TestSummaryCacheScanIsSortedSnapshot(t *testing.T) {
	ctx := context.Background()
	c := newSummaryCache()
	for _, k := range []string{"b2", "a1", "b1", "c1"} {
		_ = c.Put(ctx, "ns", k, []byte("val-"+k))
	}
	_ = c.Put(ctx, "other", "b9", []byte("elsewhere"))
	it, err := c.Scan(ctx, "ns", "b")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Put(ctx, "ns", "b3", []byte("after the snapshot"))
	var keys []string
	for it.Next(ctx) {
		keys = append(keys, it.Key())
		if string(it.Value()) != "val-"+it.Key() {
			t.Fatalf("value for %q = %q", it.Key(), it.Value())
		}
	}
	if len(keys) != 2 || keys[0] != "b1" || keys[1] != "b2" {
		t.Fatalf("scan keys = %v, want [b1 b2] in order and without the later write", keys)
	}
	if it.Err() != nil || it.Close() != nil {
		t.Fatal("iterator Err/Close must be nil")
	}
}

func TestSummaryCacheBoundClearsWhenFull(t *testing.T) {
	ctx := context.Background()
	c := newSummaryCache()
	for i := 0; i < summaryCacheMax; i++ {
		_ = c.Put(ctx, "ns", string(rune('a'+i%26))+string(rune(i)), []byte("x"))
	}
	if len(c.data) != summaryCacheMax {
		t.Fatalf("filled cache holds %d entries, want %d", len(c.data), summaryCacheMax)
	}
	_ = c.Put(ctx, "ns", "a"+string(rune(0)), []byte("overwritten")) // an existing key never clears
	if len(c.data) != summaryCacheMax {
		t.Fatalf("overwrite changed the size to %d", len(c.data))
	}
	_ = c.Put(ctx, "ns", "one-more-new-key", []byte("v"))
	if len(c.data) != 1 {
		t.Fatalf("a full cache holds %d entries after a new key, want it cleared to 1", len(c.data))
	}
}

func TestSummaryCacheTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	c := newSummaryCache()
	_ = c.Put(ctx, "ns", "keep", []byte("orig"))
	_ = c.Put(ctx, "ns", "gone", []byte("here"))
	boom := errors.New("abort")
	err := c.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		_ = tx.Put(ctx, "ns", "keep", []byte("changed"))
		_ = tx.Put(ctx, "ns", "keep", []byte("changed again")) // second touch keeps the first undo
		_ = tx.Put(ctx, "ns", "fresh", []byte("new"))
		_ = tx.Delete(ctx, "ns", "gone")
		if v, err := tx.Get(ctx, "ns", "keep"); err != nil || string(v) != "changed again" {
			t.Fatalf("tx read = %q, %v", v, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx = %v, want the callback's error", err)
	}
	if v, _ := c.Get(ctx, "ns", "keep"); string(v) != "orig" {
		t.Fatalf("rollback left keep = %q", v)
	}
	if v, _ := c.Get(ctx, "ns", "gone"); string(v) != "here" {
		t.Fatalf("rollback did not restore the deleted key: %q", v)
	}
	if _, err := c.Get(ctx, "ns", "fresh"); err == nil {
		t.Fatal("rollback left a key the transaction created")
	}
}

func TestSummaryCacheTxCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	c := newSummaryCache()
	_ = c.Put(ctx, "ns", "keep", []byte("orig"))
	err := c.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := tx.CompareAndSwap(ctx, "ns", "keep", []byte("orig"), []byte("swapped")); err != nil {
			return err
		}
		if err := tx.CompareAndSwap(ctx, "ns", "made", nil, []byte("created")); err != nil {
			return err
		}
		if err := tx.CompareAndSwap(ctx, "ns", "keep", []byte("stale"), []byte("x")); !errors.Is(err, errSummaryCAS) {
			t.Fatalf("stale CAS = %v, want errSummaryCAS", err)
		}
		if err := tx.CompareAndSwap(ctx, "ns", "keep", nil, []byte("x")); !errors.Is(err, errSummaryCAS) {
			t.Fatalf("nil-old CAS on a present key = %v, want errSummaryCAS", err)
		}
		if err := tx.CompareAndSwap(ctx, "ns", "absent", []byte("old"), []byte("x")); !errors.Is(err, errSummaryCAS) {
			t.Fatalf("CAS on an absent key = %v, want errSummaryCAS", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "ns", "keep"); !bytes.Equal(v, []byte("swapped")) {
		t.Fatalf("committed CAS left keep = %q", v)
	}
	if v, _ := c.Get(ctx, "ns", "made"); string(v) != "created" {
		t.Fatalf("committed create = %q", v)
	}
}
