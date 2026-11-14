package hj

import (
	"errors"
	"testing"
)

func TestPersistQueueStateEmptyStore(t *testing.T) {
	// NewMemoryReceiptStore is independent and empty (All()==nil)
	store1 := NewMemoryReceiptStore()
	if all := store1.All(); all != nil {
		t.Fatalf("expected nil All() for empty store1, got %v", all)
	}

	store2 := NewMemoryReceiptStore()
	if all := store2.All(); all != nil {
		t.Fatalf("expected nil All() for empty store2, got %v", all)
	}
}

func TestPersistQueueStateSaveAndRetrieve(t *testing.T) {
	store1 := NewMemoryReceiptStore()
	store2 := NewMemoryReceiptStore()

	// Save a receipt with Queue state
	r := Receipt{ID: "receipt-1", State: Queue}
	if err := store1.Save(r); err != nil {
		t.Fatalf("unexpected error saving receipt: %v", err)
	}

	// Returns the same stored receipt
	all1 := store1.All()
	if len(all1) != 1 {
		t.Fatalf("expected 1 receipt in store1, got %d", len(all1))
	}
	if all1[0].ID != r.ID || all1[0].State != r.State {
		t.Fatalf("expected %v, got %v", r, all1[0])
	}

	// Independence check: store2 must still be empty
	if all2 := store2.All(); all2 != nil {
		t.Fatalf("store2 was not independent, got %v", all2)
	}
}

func TestPersistQueueStateErrors(t *testing.T) {
	store1 := NewMemoryReceiptStore()
	r := Receipt{ID: "receipt-1", State: Queue}
	if err := store1.Save(r); err != nil {
		t.Fatalf("unexpected error saving receipt: %v", err)
	}

	// ErrDuplicateReceipt
	err := store1.Save(r)
	if !errors.Is(err, ErrDuplicateReceipt) {
		t.Fatalf("expected ErrDuplicateReceipt, got %v", err)
	}

	// ErrInvalidReceipt
	err = store1.Save(Receipt{ID: "", State: Queue})
	if !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt, got %v", err)
	}

	// ErrInvalidReceiptState
	err = store1.Save(Receipt{ID: "receipt-2", State: "Unknown"})
	if !errors.Is(err, ErrInvalidReceiptState) {
		t.Fatalf("expected ErrInvalidReceiptState, got %v", err)
	}
}

func TestPersistQueueStateSortedSnapshots(t *testing.T) {
	// All returns independent snapshots sorted ascending by ID
	store3 := NewMemoryReceiptStore()
	_ = store3.Save(Receipt{ID: "c", State: Queue})
	_ = store3.Save(Receipt{ID: "a", State: Queue})
	_ = store3.Save(Receipt{ID: "b", State: Queue})

	all3 := store3.All()
	if len(all3) != 3 {
		t.Fatalf("expected 3 receipts in store3, got %d", len(all3))
	}
	if all3[0].ID != "a" || all3[1].ID != "b" || all3[2].ID != "c" {
		t.Fatalf("expected sorted IDs [a b c], got %v", []string{all3[0].ID, all3[1].ID, all3[2].ID})
	}
}
