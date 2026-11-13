package attempt

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAppendOverflowFailsClosed covers the failure paths of the overflow sink:
// write failures, worker failures surfacing through Close, invalid input, and
// a full queue that rejects promptly and never delivers the rejected row.
func TestAppendOverflowFailsClosed(t *testing.T) {
	t.Run("missing parent", testAppendMissingParent)
	t.Run("directory at escalations path", testAppendDirectoryAtPath)
	t.Run("worker failure surfaces through Close", testWorkerFailureSurfaces)
	t.Run("invalid input before effects", testInvalidBeforeEffects)
	t.Run("full queue rejects and drains", testQueueFullRejects)
}

func testAppendMissingParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := AppendOverflowRow(dir, validRow()); !errors.Is(err, ErrOverflowWrite) {
		t.Fatalf("missing parent: err = %v, want ErrOverflowWrite", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("missing parent was created: stat err = %v", err)
	}
}

func testAppendDirectoryAtPath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "escalations.jsonl")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	prior := []byte("{\"keep\":true}\n")
	keep := filepath.Join(blocker, "keep")
	if err := os.WriteFile(keep, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendOverflowRow(dir, validRow()); !errors.Is(err, ErrOverflowWrite) {
		t.Fatalf("directory at path: err = %v, want ErrOverflowWrite", err)
	}
	got, err := os.ReadFile(keep)
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatalf("prior bytes changed: %q, %v", got, err)
	}
}

func testWorkerFailureSurfaces(t *testing.T) {
	base := &fakeBase{}
	s, err := NewOverflowSink(base, filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOverflow(validRow()); err != nil {
		t.Fatalf("RecordOverflow: %v", err)
	}
	if err := s.Close(); !errors.Is(err, ErrOverflowWrite) {
		t.Fatalf("Close = %v, want the worker's ErrOverflowWrite", err)
	}
	if base.closed != 1 {
		t.Fatalf("base.Close calls = %d, want 1", base.closed)
	}
}

func testInvalidBeforeEffects(t *testing.T) {
	calls := 0
	appendRow := func(string, OverflowRow) error { calls++; return nil }
	if _, err := newOverflowSink(nil, "d", appendRow); !errors.Is(err, ErrInvalidOverflow) {
		t.Fatalf("nil base: %v", err)
	}
	if _, err := newOverflowSink(&fakeBase{}, " ", appendRow); !errors.Is(err, ErrInvalidOverflow) {
		t.Fatalf("blank dir: %v", err)
	}
	if _, err := newOverflowSink(&fakeBase{}, "d", nil); !errors.Is(err, ErrInvalidOverflow) {
		t.Fatalf("nil appendRow: %v", err)
	}
	s, err := newOverflowSink(&fakeBase{}, "d", appendRow)
	if err != nil {
		t.Fatal(err)
	}
	bad := validRow()
	bad.Attempt = 0
	if err := s.RecordOverflow(bad); !errors.Is(err, ErrInvalidOverflow) {
		t.Fatalf("invalid row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if calls != 0 {
		t.Fatalf("appendRow ran %d times for invalid input", calls)
	}
}

// blockingAppend blocks its first call until release is closed and records
// every delivered ticket, in order.
type blockingAppend struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	got     []string
	once    sync.Once
}

func (b *blockingAppend) append(_ string, row OverflowRow) error {
	b.once.Do(func() { close(b.started); <-b.release })
	b.mu.Lock()
	b.got = append(b.got, row.Ticket)
	b.mu.Unlock()
	return nil
}

func testQueueFullRejects(t *testing.T) {
	b := &blockingAppend{started: make(chan struct{}), release: make(chan struct{})}
	s, err := newOverflowSink(&fakeBase{}, "d", b.append)
	if err != nil {
		t.Fatal(err)
	}
	row := validRow()
	if err := s.RecordOverflow(row); err != nil {
		t.Fatalf("first offer: %v", err)
	}
	<-b.started // the worker holds the first row; the buffer is empty again
	for i := 0; i < 1024; i++ {
		if err := s.RecordOverflow(row); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
	}
	rejected := validRow()
	rejected.Ticket = "REJECTED"
	if err := s.RecordOverflow(rejected); !errors.Is(err, ErrOverflowQueueFull) {
		t.Fatalf("offer past capacity = %v, want ErrOverflowQueueFull", err)
	}
	close(b.release)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(b.got) != 1025 {
		t.Fatalf("delivered %d rows, want 1025", len(b.got))
	}
	for _, tk := range b.got {
		if tk == "REJECTED" {
			t.Fatal("the rejected row was delivered")
		}
	}
}
