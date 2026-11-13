package background

import (
	"errors"
	"testing"
)

func TestSubSendQueuesJobInOrder(t *testing.T) {
	s := NewSubSend()
	if err := s.Register("chat-1", "C"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Send("chat-1", "A"); err != nil {
		t.Fatalf("Send A: %v", err)
	}
	if err := s.Send("chat-1", "B"); err != nil {
		t.Fatalf("Send B: %v", err)
	}
	got := s.Run("chat-1")
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("Run after A,B = %v, want [A B]", got)
	}
	got2 := s.Run("chat-1")
	if got2 == nil {
		t.Fatalf("second Run = nil, want non-nil empty slice")
	}
	if len(got2) != 0 {
		t.Fatalf("second Run = %v, want empty slice", got2)
	}
}

func TestSubSendRejectsEmptyJob(t *testing.T) {
	s := NewSubSend()
	if err := s.Register("chat-1", "C"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := s.Send("chat-1", "   ")
	if !errors.Is(err, ErrEmptyJob) {
		t.Fatalf("Send whitespace: %v, want ErrEmptyJob", err)
	}
	if err.(Error).Code != "D6100" {
		t.Fatalf("error code = %q, want D6100", err.(Error).Code)
	}
	if err.Error() != "D6100: empty job" {
		t.Fatalf("error msg = %q, want D6100: empty job", err.Error())
	}

	// verify queue unchanged
	got := s.Run("chat-1")
	if len(got) != 0 {
		t.Fatalf("queue after rejected send = %v, want empty", got)
	}
}
