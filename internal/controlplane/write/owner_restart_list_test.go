package write

import (
	"testing"
	"time"
)

func TestOwnerRestartsOrderedByTime(t *testing.T) {
	// Nil store yields nonnil empty slice
	var nilStore *AuditStore
	if got := nilStore.List("owner_restart"); got == nil || len(got) != 0 {
		t.Fatalf("nil store List: got %v, want non-nil empty slice", got)
	}

	// Zero value store and unknown kind yield nonnil empty slice
	var store AuditStore
	if got := store.List("unknown_kind"); got == nil || len(got) != 0 {
		t.Fatalf("unknown kind List: got %v, want non-nil empty slice", got)
	}

	// Record at 10:05 then 10:00
	t1 := time.Date(2023, 1, 1, 10, 5, 0, 0, time.UTC)
	t2 := time.Date(2023, 1, 1, 10, 0, 0, 0, time.UTC)

	_ = store.Append(AuditEvent{Kind: "owner_restart", Item: "item_late", At: t1})
	_ = store.Append(AuditEvent{Kind: "owner_restart", Item: "item_early", At: t2})

	// Listings exclude process_restart
	_ = store.Append(AuditEvent{Kind: "process_restart", Item: "proc", At: t1})

	got := store.List("owner_restart")
	if got == nil {
		t.Fatalf("List returned nil, want non-nil slice")
	}
	if len(got) != 2 {
		t.Fatalf("List length: got %d, want 2", len(got))
	}

	// Ascending UTC order
	if got[0].Item != "item_early" || got[1].Item != "item_late" {
		t.Errorf("List order: got %s then %s, want item_early then item_late", got[0].Item, got[1].Item)
	}

	// Equal timestamps keep insertion order
	t3 := time.Date(2023, 1, 1, 11, 0, 0, 0, time.UTC)
	var store2 AuditStore
	_ = store2.Append(AuditEvent{Kind: "owner_restart", Item: "first", At: t3})
	_ = store2.Append(AuditEvent{Kind: "owner_restart", Item: "second", At: t3})

	got2 := store2.List("owner_restart")
	if got2[0].Item != "first" || got2[1].Item != "second" {
		t.Errorf("Equal timestamps order: got %s then %s, want first then second", got2[0].Item, got2[1].Item)
	}

	// Result mutation cannot change storage
	got[0].Item = "mutated"
	gotCheck := store.List("owner_restart")
	if gotCheck[0].Item != "item_early" {
		t.Errorf("Storage mutated: got %s, want item_early", gotCheck[0].Item)
	}
}
