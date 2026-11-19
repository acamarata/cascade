package write

import (
	"bytes"
	"testing"
)

type fakeStore struct {
	val   int
	calls int
}

func (f *fakeStore) Get() (int, error) {
	f.calls++
	return f.val, nil
}

func (f *fakeStore) Set(n int) error {
	f.val = n
	return nil
}

func TestGreensDecreaseExitsOneAndPrintsDropped(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{val: 10}
	Baselines = store
	exit := RunGreens([]string{}, 8, 10, &buf)
	if exit != 1 {
		t.Fatalf("expected 1, got %d", exit)
	}
	if !bytes.Contains(buf.Bytes(), []byte("dropped")) {
		t.Fatalf("expected 'dropped' in output, got %q", buf.String())
	}
}

func TestGreensEqualOrHigherExitsZero(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{val: 10}
	Baselines = store
	exit := RunGreens([]string{}, 10, 10, &buf)
	if exit != 0 {
		t.Fatalf("expected 0, got %d", exit)
	}
	exit = RunGreens([]string{}, 12, 10, &buf)
	if exit != 0 {
		t.Fatalf("expected 0, got %d", exit)
	}
}

func TestGreensRebaseUpdatesBaseline(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{val: 10}
	Baselines = store
	exit := RunGreens([]string{"--rebase"}, 8, 10, &buf)
	if exit != 0 {
		t.Fatalf("expected 0, got %d", exit)
	}
	if store.val != 8 {
		t.Fatalf("expected store to hold 8, got %d", store.val)
	}
}

func TestGreensUnknownFlagExitsTwo(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{val: 10}
	Baselines = store
	exit := RunGreens([]string{"--bogus"}, 8, 10, &buf)
	if exit != 2 {
		t.Fatalf("expected 2, got %d", exit)
	}
	if store.val != 10 {
		t.Fatalf("expected store to be unchanged (10), got %d", store.val)
	}
}
