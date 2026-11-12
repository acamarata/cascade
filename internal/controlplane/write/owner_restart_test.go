package write

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOwnerRestartEmptyItemRejected(t *testing.T) {
	fixedTime := time.Date(2026, 11, 12, 10, 0, 0, 123456789, time.UTC)

	tests := []struct {
		name  string
		store *AuditStore
		event AuditEvent
	}{
		{
			name:  "nil store",
			store: nil,
			event: AuditEvent{
				Kind: "owner_restart",
				Item: "test",
				At:   fixedTime,
			},
		},
		{
			name:  "empty item",
			store: &AuditStore{},
			event: AuditEvent{
				Kind: "owner_restart",
				Item: "",
				At:   fixedTime,
			},
		},
		{
			name:  "whitespace only item",
			store: &AuditStore{},
			event: AuditEvent{
				Kind: "owner_restart",
				Item: "   ",
				At:   fixedTime,
			},
		},
		{
			name:  "whitespace padded item",
			store: &AuditStore{},
			event: AuditEvent{
				Kind: "owner_restart",
				Item: "  test  ",
				At:   fixedTime,
			},
		},
		{
			name:  "zero timestamp",
			store: &AuditStore{},
			event: AuditEvent{
				Kind: "owner_restart",
				Item: "test",
				At:   time.Time{},
			},
		},
		{
			name:  "invalid kind",
			store: &AuditStore{},
			event: AuditEvent{
				Kind: "invalid_kind",
				Item: "test",
				At:   fixedTime,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.store.Append(tt.event)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidRestart) {
				t.Errorf("expected ErrInvalidRestart, got %v", err)
			}
			if !strings.Contains(err.Error(), "invalid restart event") {
				t.Errorf("expected error message to contain 'invalid restart event', got %q", err.Error())
			}

			// Verify no mutation occurred
			if tt.store != nil && len(tt.store.events) > 0 {
				t.Errorf("expected no mutation, but store has %d events", len(tt.store.events))
			}
		})
	}

	// Test that zero-value store is usable
	t.Run("zero value store is usable", func(t *testing.T) {
		var store AuditStore
		event := AuditEvent{
			Kind: "owner_restart",
			Item: "test",
			At:   fixedTime,
		}
		err := store.Append(event)
		if err != nil {
			t.Errorf("expected nil error for valid event, got %v", err)
		}
		if len(store.events) != 1 {
			t.Errorf("expected 1 event, got %d", len(store.events))
		}
	})

	// Test valid kinds
	t.Run("valid kinds", func(t *testing.T) {
		store := &AuditStore{}

		err := store.Append(AuditEvent{
			Kind: "owner_restart",
			Item: "test1",
			At:   fixedTime,
		})
		if err != nil {
			t.Errorf("owner_restart should be valid, got error: %v", err)
		}

		err = store.Append(AuditEvent{
			Kind: "process_restart",
			Item: "test2",
			At:   fixedTime,
		})
		if err != nil {
			t.Errorf("process_restart should be valid, got error: %v", err)
		}

		if len(store.events) != 2 {
			t.Errorf("expected 2 events, got %d", len(store.events))
		}
	})

	// Test UTC conversion preserves nanoseconds
	t.Run("UTC conversion preserves nanoseconds", func(t *testing.T) {
		store := &AuditStore{}
		loc, _ := time.LoadLocation("America/New_York")
		localTime := time.Date(2026, 11, 12, 10, 0, 0, 123456789, loc)

		err := store.Append(AuditEvent{
			Kind: "owner_restart",
			Item: "test",
			At:   localTime,
		})
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}

		stored := store.events[0]
		if stored.At.Location() != time.UTC {
			t.Errorf("expected UTC location, got %v", stored.At.Location())
		}
		if stored.At.Nanosecond() != 123456789 {
			t.Errorf("expected nanoseconds preserved (123456789), got %d", stored.At.Nanosecond())
		}
	})

	// Test insertion order is maintained
	t.Run("insertion order maintained", func(t *testing.T) {
		store := &AuditStore{}

		for i := 0; i < 3; i++ {
			err := store.Append(AuditEvent{
				Kind: "owner_restart",
				Item: strings.Repeat("x", i+1),
				At:   fixedTime.Add(time.Duration(i) * time.Second),
			})
			if err != nil {
				t.Errorf("expected nil error for event %d, got %v", i, err)
			}
		}

		if len(store.events) != 3 {
			t.Errorf("expected 3 events, got %d", len(store.events))
		}

		for i := 0; i < 3; i++ {
			expected := strings.Repeat("x", i+1)
			if store.events[i].Item != expected {
				t.Errorf("event %d: expected item %q, got %q", i, expected, store.events[i].Item)
			}
		}
	})
}
