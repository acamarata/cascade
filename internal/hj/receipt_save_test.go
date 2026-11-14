package hj

import (
	"errors"
	"testing"
)

func TestPersistInvalidReceipt(t *testing.T) {
	tests := []struct {
		name    string
		receipt Receipt
		err     error
	}{
		{
			name:    "unknown state",
			receipt: Receipt{ID: "1", State: "Unknown"},
			err:     ErrInvalidReceiptState,
		},
		{
			name:    "empty state",
			receipt: Receipt{ID: "2", State: ""},
			err:     ErrInvalidReceiptState,
		},
		{
			name:    "empty id",
			receipt: Receipt{ID: "", State: Queue},
			err:     ErrInvalidReceipt,
		},
		{
			name:    "whitespace id",
			receipt: Receipt{ID: "   ", State: Queue},
			err:     ErrInvalidReceipt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryReceiptStore()
			err := store.Save(tt.receipt)
			if !errors.Is(err, tt.err) {
				t.Fatalf("expected error %v, got %v", tt.err, err)
			}
			if err != nil && err.Error() != tt.err.Error() {
				t.Fatalf("expected error message %q, got %q", tt.err.Error(), err.Error())
			}
			if all := store.All(); all != nil {
				t.Fatalf("expected empty store, got %v", all)
			}
		})
	}
}

func TestPersistDuplicateReceipt(t *testing.T) {
	store := NewMemoryReceiptStore()
	r1 := Receipt{ID: "dup-id", State: Queue}
	if err := store.Save(r1); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}

	r2 := Receipt{ID: "dup-id", State: Taken}
	err := store.Save(r2)
	if !errors.Is(err, ErrDuplicateReceipt) {
		t.Fatalf("expected ErrDuplicateReceipt, got %v", err)
	}
	if err.Error() != "receipt: duplicate id" {
		t.Fatalf("expected error message %q, got %q", "receipt: duplicate id", err.Error())
	}

	all := store.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(all))
	}
	if all[0].State != Queue {
		t.Fatalf("expected original state Queue, got %s", all[0].State)
	}
}
