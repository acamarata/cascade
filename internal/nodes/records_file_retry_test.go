package nodes

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

var errTransientFake = errors.New("fake transient share conflict")

func isFakeTransient(err error) bool { return errors.Is(err, errTransientFake) }

// recordingSleep returns a sleep stand-in that records every delay and
// never blocks.
func recordingSleep(delays *[]time.Duration) func(time.Duration) {
	return func(d time.Duration) { *delays = append(*delays, d) }
}

// TestRetryRecordFileOp_TransientThenSuccess: a conflict that clears is
// ridden out with doubling backoff and the op's success is returned.
func TestRetryRecordFileOp_TransientThenSuccess(t *testing.T) {
	var delays []time.Duration
	calls := 0
	err := retryRecordFileOp(isFakeTransient, recordingSleep(&delays), func() error {
		calls++
		if calls <= 3 {
			return errTransientFake
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

// TestRetryRecordFileOp_BudgetExhaustedFailsClosed: a conflict that never
// clears returns the last error itself after exactly recordFileAttempts
// tries, with the backoff capped.
func TestRetryRecordFileOp_BudgetExhaustedFailsClosed(t *testing.T) {
	var delays []time.Duration
	calls := 0
	err := retryRecordFileOp(isFakeTransient, recordingSleep(&delays), func() error {
		calls++
		return errTransientFake
	})
	if !errors.Is(err, errTransientFake) {
		t.Fatalf("err = %v, want the transient error returned unchanged", err)
	}
	if calls != recordFileAttempts {
		t.Fatalf("calls = %d, want %d", calls, recordFileAttempts)
	}
	if len(delays) != recordFileAttempts-1 {
		t.Fatalf("sleeps = %d, want %d", len(delays), recordFileAttempts-1)
	}
	if last := delays[len(delays)-1]; last != recordFileMaxDelay {
		t.Fatalf("last delay = %v, want cap %v", last, recordFileMaxDelay)
	}
}

// TestRetryRecordFileOp_NonTransientReturnsAtOnce: any other error is
// never retried.
func TestRetryRecordFileOp_NonTransientReturnsAtOnce(t *testing.T) {
	var delays []time.Duration
	calls := 0
	hard := errors.New("hard failure")
	err := retryRecordFileOp(isFakeTransient, recordingSleep(&delays), func() error {
		calls++
		return hard
	})
	if !errors.Is(err, hard) || calls != 1 || len(delays) != 0 {
		t.Fatalf("err=%v calls=%d sleeps=%d, want hard failure after 1 call, 0 sleeps", err, calls, len(delays))
	}
}

// TestRecordFileLock_SharedPerCleanPath: two spellings of one file share
// a lock; another file does not.
func TestRecordFileLock_SharedPerCleanPath(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "nodes", "devices.json")
	messy := filepath.Join(dir, "nodes", "..", "nodes", ".", "devices.json")
	if recordFileLock(clean) != recordFileLock(messy) {
		t.Fatal("two spellings of one path got different locks")
	}
	t.Chdir(dir)
	if recordFileLock(clean) != recordFileLock(filepath.Join("nodes", "devices.json")) {
		t.Fatal("relative and absolute spellings of one path got different locks")
	}
	if recordFileLock(clean) == recordFileLock(filepath.Join(dir, "other.json")) {
		t.Fatal("two different paths share a lock")
	}
}

// TestFileRecordBackend_ConcurrentSaveLoad is the in-process shape that
// went red on windows/amd64: one backend rewriting devices.json (the
// presence probe loop) while a second backend over the same data dir in
// the same process reads it (the RPC handler). It guards the per-path
// lock: every Load must succeed and see a complete record set. On
// Windows, without that lock, a Load here can fail with
// ERROR_SHARING_VIOLATION. Cross-process conflicts are covered by the
// held-handle tests in records_file_windows_test.go.
func TestFileRecordBackend_ConcurrentSaveLoad(t *testing.T) {
	dataDir := t.TempDir()
	writer := NewFileRecordBackend(dataDir)
	reader := NewFileRecordBackend(dataDir)
	seed := map[string]DeviceRecord{"n1": {NodeID: "n1"}}
	if err := writer.Save(seed); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	const rounds = 200
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range rounds {
			if err := writer.Save(seed); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range rounds {
			got, err := reader.Load()
			if err != nil {
				errs <- err
				continue
			}
			if _, ok := got["n1"]; !ok {
				errs <- errors.New("Load returned a record set without n1")
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Save/Load: %v", err)
	}
	if t.Failed() {
		return
	}
	entries, err := filepath.Glob(filepath.Join(dataDir, "nodes", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, e := range entries {
		if runtime.IsAtomicTempName(filepath.Base(e)) {
			t.Errorf("leftover atomic temp file %s", e)
		}
	}
}
