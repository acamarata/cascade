package background

import (
	"errors"
	"testing"
)

func TestRecoveryInvalidInput(t *testing.T) {
	noopList := func() ([]string, error) { return nil, nil }
	noopProcess := func(string) error { return nil }

	tests := []struct {
		name       string
		listDirs   func() ([]string, error)
		processDir func(string) error
		capacity   int
	}{
		{
			name:       "nil listDirs",
			listDirs:   nil,
			processDir: noopProcess,
			capacity:   1,
		},
		{
			name:       "nil processDir",
			listDirs:   noopList,
			processDir: nil,
			capacity:   1,
		},
		{
			name:       "negative capacity",
			listDirs:   noopList,
			processDir: noopProcess,
			capacity:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewSlotRecoverer(tt.listDirs, tt.processDir, tt.capacity)
			if !errors.Is(err, ErrInvalidRecovery) {
				t.Fatalf("expected ErrInvalidRecovery, got %v", err)
			}
			if r != nil {
				t.Fatal("expected nil recoverer")
			}
		})
	}
}

func TestRecoveryZeroValueAndValidation(t *testing.T) {
	t.Run("zero value Recover", func(t *testing.T) {
		var r SlotRecoverer
		err := r.Recover()
		if !errors.Is(err, ErrInvalidRecovery) {
			t.Fatalf("expected ErrInvalidRecovery from zero-value Recover, got %v", err)
		}
	})

	t.Run("empty directory names", func(t *testing.T) {
		processed := false
		listEmpty := func() ([]string, error) { return []string{""}, nil }
		process := func(string) error { processed = true; return nil }

		r, err := NewSlotRecoverer(listEmpty, process, 1)
		if err != nil {
			t.Fatalf("constructor failed: %v", err)
		}
		err = r.Recover()
		if !errors.Is(err, ErrInvalidRecovery) {
			t.Fatalf("expected ErrInvalidRecovery for empty dir, got %v", err)
		}
		if processed {
			t.Fatal("no processing should occur on list validation failure")
		}
	})

	t.Run("duplicate directory names", func(t *testing.T) {
		processed := false
		listDup := func() ([]string, error) { return []string{"a", "a"}, nil }
		process := func(string) error { processed = true; return nil }

		r, err := NewSlotRecoverer(listDup, process, 2)
		if err != nil {
			t.Fatalf("constructor failed: %v", err)
		}
		err = r.Recover()
		if !errors.Is(err, ErrInvalidRecovery) {
			t.Fatalf("expected ErrInvalidRecovery for duplicate dirs, got %v", err)
		}
		if processed {
			t.Fatal("no processing should occur on list validation failure")
		}
	})

}

func TestRecoveryListErrorAndCapacity(t *testing.T) {
	noopList := func() ([]string, error) { return nil, nil }
	noopProcess := func(string) error { return nil }

	t.Run("list error propagates and keeps slots closed", func(t *testing.T) {
		listErr := errors.New("list failed")
		listFail := func() ([]string, error) { return nil, listErr }
		process := func(string) error { return nil }

		r, err := NewSlotRecoverer(listFail, process, 1)
		if err != nil {
			t.Fatalf("constructor failed: %v", err)
		}
		err = r.Recover()
		if !errors.Is(err, listErr) {
			t.Fatalf("expected list error to propagate, got %v", err)
		}
		// Subsequent call on closed recoverer returns ErrInvalidRecovery.
		err = r.Recover()
		if !errors.Is(err, ErrInvalidRecovery) {
			t.Fatalf("expected ErrInvalidRecovery after list failure, got %v", err)
		}
	})

	t.Run("capacity zero is valid", func(t *testing.T) {
		r, err := NewSlotRecoverer(noopList, noopProcess, 0)
		if err != nil {
			t.Fatalf("expected no error for capacity 0, got %v", err)
		}
		if r == nil {
			t.Fatal("expected non-nil recoverer")
		}
	})
}
