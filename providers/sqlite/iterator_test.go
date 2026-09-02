// Purpose: Scan/Iterator edge cases beyond storetest's basic prefix case —
//
//	the unbounded-prefix fallback (prefix ending in 0xFF, no representable
//	successor) and Iterator.Close idempotency. Split from driver_test.go
//	under R-14.117.
//
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).
package sqlite_test

import (
	"context"
	"testing"
)

// TestDriver_Scan_UnboundedPrefixFallback exercises prefixRange's "no
// successor" branch: a prefix whose last byte is 0xFF has no same-length
// successor, so the scan falls back to an open lower-bound-only query.
func TestDriver_Scan_UnboundedPrefixFallback(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	prefix := "p\xff"
	if err := d.Put(ctx, "ns", prefix+"1", []byte("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := d.Put(ctx, "ns", "q-other", []byte("b")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	it, err := d.Scan(ctx, "ns", prefix)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := map[string][]byte{}
	for it.Next(ctx) {
		got[it.Key()] = it.Value()
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("second Close (idempotent): %v", err)
	}
	if _, ok := got[prefix+"1"]; !ok {
		t.Fatalf("Scan(%q) missing expected key, got %v", prefix, got)
	}
}

// TestDriver_Scan_EmptyPrefixWholeNamespace exercises Scan's empty-prefix
// branch (newScanIterator's "prefix == \"\"" case), distinct from the
// bounded-prefix case storetest.RunStoreTests already covers.
func TestDriver_Scan_EmptyPrefixWholeNamespace(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	want := map[string][]byte{"a": []byte("1"), "b": []byte("2")}
	for k, v := range want {
		if err := d.Put(ctx, "whole-ns", k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	it, err := d.Scan(ctx, "whole-ns", "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(ctx) {
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if n != len(want) {
		t.Fatalf("Scan(\"\") returned %d entries, want %d", n, len(want))
	}
}
