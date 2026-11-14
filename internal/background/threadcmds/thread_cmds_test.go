package threadcmds

import (
	"errors"
	"testing"
	"time"
)

type fakeClock struct{}

func (f fakeClock) After(_ time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

type memThreadStore struct {
	threads map[string][]string
}

func newMemStore() *memThreadStore {
	return &memThreadStore{threads: make(map[string][]string)}
}

func (m *memThreadStore) Append(threadID, message string) error {
	if _, ok := m.threads[threadID]; !ok {
		if threadID == "nope" {
			return ErrNoThread
		}
		return ErrNoThread
	}
	m.threads[threadID] = append(m.threads[threadID], message)
	return nil
}

func (m *memThreadStore) Done(threadID string) (<-chan struct{}, error) {
	if _, ok := m.threads[threadID]; !ok {
		return nil, ErrNoThread
	}
	ch := make(chan struct{})
	close(ch)
	return ch, nil
}

func (m *memThreadStore) Result(threadID string) (string, bool, error) {
	if _, ok := m.threads[threadID]; !ok {
		return "", false, ErrNoThread
	}
	return "", true, nil
}

func TestSendAppendsMessage(t *testing.T) {
	store := newMemStore()
	store.threads["t1"] = []string{}

	cmds, err := NewThreadCommands(store, fakeClock{})
	if err != nil {
		t.Fatalf("NewThreadCommands returned error: %v", err)
	}

	err = cmds.Send("t1", "hello")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if len(store.threads["t1"]) != 1 || store.threads["t1"][0] != "hello" {
		t.Fatalf("thread log does not contain 'hello', got %v", store.threads["t1"])
	}
}

func TestUnknownThreadFails(t *testing.T) {
	store := newMemStore()
	cmds, err := NewThreadCommands(store, fakeClock{})
	if err != nil {
		t.Fatalf("NewThreadCommands returned error: %v", err)
	}

	err = cmds.Send("nope", "hello")
	if !errors.Is(err, ErrNoThread) {
		t.Fatalf("expected ErrNoThread, got %v", err)
	}
}

func TestThreadCommandBoundaries(t *testing.T) {
	store := newMemStore()
	store.threads["t1"] = []string{}

	_, err := NewThreadCommands(nil, fakeClock{})
	if !errors.Is(err, ErrInvalidThreadInput) {
		t.Fatalf("expected ErrInvalidThreadInput for nil store, got %v", err)
	}

	_, err = NewThreadCommands(store, nil)
	if !errors.Is(err, ErrInvalidThreadInput) {
		t.Fatalf("expected ErrInvalidThreadInput for nil clock, got %v", err)
	}

	cmds, err := NewThreadCommands(store, fakeClock{})
	if err != nil {
		t.Fatalf("NewThreadCommands returned error: %v", err)
	}

	err = cmds.Send("", "hello")
	if !errors.Is(err, ErrInvalidThreadInput) {
		t.Fatalf("expected ErrInvalidThreadInput for empty threadID, got %v", err)
	}

	err = cmds.Send("t1", "")
	if !errors.Is(err, ErrInvalidThreadInput) {
		t.Fatalf("expected ErrInvalidThreadInput for empty message, got %v", err)
	}

	// whitespace is literal valid content
	err = cmds.Send("t1", " ")
	if err != nil {
		t.Fatalf("expected whitespace to be valid, got %v", err)
	}
	if len(store.threads["t1"]) != 1 || store.threads["t1"][0] != " " {
		t.Fatalf("thread log does not contain whitespace, got %v", store.threads["t1"])
	}
}
