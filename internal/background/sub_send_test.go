package background

import (
	"errors"
	"testing"
)

func TestSubSendPreservesContext(t *testing.T) {
	t.Run("zero value is usable", func(t *testing.T) {
		var s SubSend
		err := s.Register("chat1", "context1")
		if err != nil {
			t.Fatalf("Register on zero value failed: %v", err)
		}
		if got := s.Context("chat1"); got != "context1" {
			t.Errorf("Context = %q, want %q", got, "context1")
		}
	})

	t.Run("empty context is valid", func(t *testing.T) {
		var s SubSend
		err := s.Register("chat2", "")
		if err != nil {
			t.Fatalf("Register with empty context failed: %v", err)
		}
		if got := s.Context("chat2"); got != "" {
			t.Errorf("Context = %q, want empty string", got)
		}
	})

	t.Run("nil receiver returns ErrInvalidChat", func(t *testing.T) {
		var s *SubSend
		err := s.Register("chat3", "context3")
		if !errors.Is(err, ErrInvalidChat) {
			t.Errorf("Register on nil receiver: got %v, want ErrInvalidChat", err)
		}
	})

	t.Run("blank ID returns ErrInvalidChat", func(t *testing.T) {
		var s SubSend
		testCases := []string{"", "   ", "\t", "\n"}
		for _, id := range testCases {
			err := s.Register(id, "context")
			if !errors.Is(err, ErrInvalidChat) {
				t.Errorf("Register with blank ID %q: got %v, want ErrInvalidChat", id, err)
			}
		}
	})

	t.Run("duplicate registration returns ErrDuplicateChat", func(t *testing.T) {
		var s SubSend
		err := s.Register("chat4", "context4")
		if err != nil {
			t.Fatalf("First Register failed: %v", err)
		}
		err = s.Register("chat4", "context4-duplicate")
		if !errors.Is(err, ErrDuplicateChat) {
			t.Errorf("Duplicate Register: got %v, want ErrDuplicateChat", err)
		}
		// Verify original context preserved
		if got := s.Context("chat4"); got != "context4" {
			t.Errorf("Context after duplicate = %q, want %q", got, "context4")
		}
	})

	t.Run("context preserved after multiple registrations", func(t *testing.T) {
		var s SubSend
		registrations := []struct {
			chatID string
			ctx    string
		}{
			{"chatA", "contextA"},
			{"chatB", "contextB"},
			{"chatC", "contextC"},
			{"chatD", "contextD"},
		}
		for _, reg := range registrations {
			err := s.Register(reg.chatID, reg.ctx)
			if err != nil {
				t.Fatalf("Register %q failed: %v", reg.chatID, err)
			}
		}
		// Verify all contexts preserved
		for _, reg := range registrations {
			if got := s.Context(reg.chatID); got != reg.ctx {
				t.Errorf("Context(%q) = %q, want %q", reg.chatID, got, reg.ctx)
			}
		}
	})

	t.Run("unknown chat returns empty string", func(t *testing.T) {
		var s SubSend
		if got := s.Context("unknown"); got != "" {
			t.Errorf("Context on unknown = %q, want empty string", got)
		}
	})

	t.Run("context on nil receiver returns empty string", func(t *testing.T) {
		var s *SubSend
		if got := s.Context("chat5"); got != "" {
			t.Errorf("Context on nil = %q, want empty string", got)
		}
	})

	t.Run("NewSubSend creates initialized instance", func(t *testing.T) {
		s := NewSubSend()
		if s == nil {
			t.Fatal("NewSubSend returned nil")
		}
		err := s.Register("chat6", "context6")
		if err != nil {
			t.Fatalf("Register on NewSubSend failed: %v", err)
		}
		if got := s.Context("chat6"); got != "context6" {
			t.Errorf("Context = %q, want %q", got, "context6")
		}
	})

	t.Run("error types match correctly", func(t *testing.T) {
		var s SubSend

		// Test ErrInvalidChat matching
		err := s.Register("", "context")
		if !errors.Is(err, ErrInvalidChat) {
			t.Error("ErrInvalidChat did not match")
		}
		if errors.Is(err, ErrDuplicateChat) {
			t.Error("ErrInvalidChat incorrectly matched ErrDuplicateChat")
		}

		// Test ErrDuplicateChat matching
		s.Register("chat7", "context7")
		err = s.Register("chat7", "duplicate")
		if !errors.Is(err, ErrDuplicateChat) {
			t.Error("ErrDuplicateChat did not match")
		}
		if errors.Is(err, ErrInvalidChat) {
			t.Error("ErrDuplicateChat incorrectly matched ErrInvalidChat")
		}
	})

	t.Run("error string format", func(t *testing.T) {
		err := ErrInvalidChat
		expected := "D6100: invalid chat"
		if got := err.Error(); got != expected {
			t.Errorf("Error() = %q, want %q", got, expected)
		}
	})
}
