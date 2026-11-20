package threadcmds

import (
	"errors"
	"testing"
	"time"
)

// --- additional fakes for uncovered branches ---

type nilDoneStore struct{}

func (nilDoneStore) Append(string, string) error          { return nil }
func (nilDoneStore) Done(string) (<-chan struct{}, error) { return nil, nil }
func (nilDoneStore) Result(string) (string, bool, error)  { return "", false, nil }

type errDoneStore struct{ err error }

func (errDoneStore) Append(string, string) error            { return nil }
func (s errDoneStore) Done(string) (<-chan struct{}, error) { return nil, s.err }
func (errDoneStore) Result(string) (string, bool, error)    { return "", false, nil }

type blockingClock struct{ ch chan time.Time }

func (b *blockingClock) After(time.Duration) <-chan time.Time { return b.ch }

var errSentinel = errors.New("store done error")

func TestWaitInvalidInputs(t *testing.T) {
	store := &waitMemStore{doneCh: make(chan struct{})}
	clock := &fakeWaitClock{C: make(chan time.Time)}

	cmds, err := NewThreadCommands(store, clock)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	tests := []struct {
		name    string
		recv    *ThreadCommands
		timeout time.Duration
	}{
		{"nil receiver", nil, time.Second},
		{"zero timeout", cmds, 0},
		{"negative timeout", cmds, -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.recv.Wait("t1", tt.timeout)
			if !errors.Is(err, ErrInvalidThreadInput) {
				t.Fatalf("expected ErrInvalidThreadInput, got %v", err)
			}
		})
	}
}

func TestWaitNilChannelReturnsError(t *testing.T) {
	clock := &fakeWaitClock{C: make(chan time.Time)}
	cmds, err := NewThreadCommands(nilDoneStore{}, clock)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	err = cmds.Wait("t1", time.Second)
	if !errors.Is(err, ErrInvalidThreadInput) {
		t.Fatalf("expected ErrInvalidThreadInput for nil channel, got %v", err)
	}
}

func TestWaitPropagatesDoneError(t *testing.T) {
	clock := &fakeWaitClock{C: make(chan time.Time)}
	cmds, err := NewThreadCommands(errDoneStore{err: errSentinel}, clock)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	err = cmds.Wait("t1", time.Second)
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestWaitTimeoutRecheckFallsThrough(t *testing.T) {
	ch := make(chan struct{}) // open, never closes
	store := &waitMemStore{doneCh: ch}
	bc := &blockingClock{ch: make(chan time.Time, 1)}
	bc.ch <- time.Now() // timeout fires immediately

	cmds, err := NewThreadCommands(store, bc)
	if err != nil {
		t.Fatalf("NewThreadCommands: %v", err)
	}

	err = cmds.Wait("t1", time.Second)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout on recheck fallthrough, got %v", err)
	}
}
