package threadcmds

import (
	"errors"
	"testing"
	"time"
)

type fakeWaitClock struct {
	C chan time.Time
}

func (f *fakeWaitClock) After(d time.Duration) <-chan time.Time {
	return f.C
}

type waitMemStore struct {
	doneCh chan struct{}
}

func (m *waitMemStore) Append(threadID, message string) error { return nil }
func (m *waitMemStore) Done(threadID string) (<-chan struct{}, error) {
	return m.doneCh, nil
}
func (m *waitMemStore) Result(threadID string) (string, bool, error) {
	return "", false, nil
}

func TestWaitReturnsOnDone(t *testing.T) {
	ch := make(chan struct{})
	close(ch)
	store := &waitMemStore{doneCh: ch}
	clock := &fakeWaitClock{C: make(chan time.Time)}

	cmds, err := NewThreadCommands(store, clock)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	err = cmds.Wait("t1", time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestWaitTimeout(t *testing.T) {
	ch := make(chan struct{})
	store := &waitMemStore{doneCh: ch}
	clock := &fakeWaitClock{C: make(chan time.Time, 1)}

	cmds, err := NewThreadCommands(store, clock)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		clock.C <- time.Now()
	}()

	err = cmds.Wait("t1", 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}

	// Verify ready completion wins over simultaneously ready timeout
	ch2 := make(chan struct{})
	close(ch2)
	store2 := &waitMemStore{doneCh: ch2}
	clock2 := &fakeWaitClock{C: make(chan time.Time, 1)}
	clock2.C <- time.Now() // timeout is already fired

	cmds2, err := NewThreadCommands(store2, clock2)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	err = cmds2.Wait("t1", time.Second)
	if err != nil {
		t.Fatalf("Wait should succeed on closed channel even if timeout is ready: %v", err)
	}
}
