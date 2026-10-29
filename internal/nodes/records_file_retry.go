package nodes

// Purpose: keep devices.json readable while it is being rewritten.
// Inputs: the record file path, and the read or write to attempt.
// Outputs: the operation's own result, after at most recordFileAttempts
//   tries.
// Constraints: Go's os.Open on Windows shares only READ|WRITE, never
//   DELETE, and MoveFileEx replacing the target briefly holds it without
//   read sharing. So a reader racing the atomic rename in Save fails with
//   ERROR_SHARING_VIOLATION, and the rename fails with ERROR_ACCESS_DENIED
//   while a reader holds the file. On Windows, Load reads through
//   readRecordFile, which adds FILE_SHARE_DELETE (fleet/tailer precedent)
//   so its handle is the least restrictive a reader can hold. Whether a
//   replace-rename then succeeds depends on the rename semantics NTFS
//   applies, so the conflicts are still treated as transient. Two layers
//   handle them:
//   1. recordFileLock serializes readers against the writer for one path
//      inside this process (the node serve daemon's probe loop writing
//      while its RPC handlers read). That removes the in-process race.
//   2. retryRecordFileOp retries a transient conflict with another
//      process (a `cascade node list` racing the daemon) a bounded number
//      of times. isTransientShareError is false for every error off
//      Windows, so POSIX behavior is unchanged.
//   Fail closed: a non-transient error returns at once, and a conflict
//   that outlasts the budget returns the last error unchanged. Nothing is
//   swallowed or replaced with an empty fleet.
// SPORT: internal/nodes records file backend (CHG): windows sharing retry.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	recordFileAttempts  = 8
	recordFileBaseDelay = 5 * time.Millisecond
	recordFileMaxDelay  = 100 * time.Millisecond
)

// recordFileLocks maps an absolute record file path to its *sync.RWMutex.
var recordFileLocks sync.Map

// recordFileLock returns the process-wide lock for path. Every
// fileRecordBackend over the same file shares it, however many stores the
// composition roots build, and whether they were handed a relative or an
// absolute data dir.
func recordFileLock(path string) *sync.RWMutex {
	key, err := filepath.Abs(path)
	if err != nil {
		key = filepath.Clean(path)
	}
	mu, _ := recordFileLocks.LoadOrStore(foldLockKey(key), &sync.RWMutex{})
	return mu.(*sync.RWMutex)
}

// retryRecordFileOp runs op until it succeeds, fails with an error
// isTransient rejects, or has run recordFileAttempts times. It sleeps
// with doubling backoff (capped at recordFileMaxDelay, about 355ms in all)
// between attempts and returns op's last error unchanged.
func retryRecordFileOp(isTransient func(error) bool, sleep func(time.Duration), op func() error) error {
	delay := recordFileBaseDelay
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil || attempt >= recordFileAttempts || !isTransient(err) {
			return err
		}
		sleep(delay)
		delay = min(delay*2, recordFileMaxDelay)
	}
}

// readWithFallback reads path through primary. When primary reports the
// file missing, it asks os.ReadFile instead and returns that result
// unchanged. The windows primary calls CreateFile on the raw path, which
// skips the \\?\ long-path fixup os applies, so a path past MAX_PATH would
// read as not-exist and Load would report an empty fleet that the next
// Save publishes. os stays the one authority on "does not exist": a file
// that is really missing still reports not-exist.
func readWithFallback(primary func(string) ([]byte, error), path string) ([]byte, error) {
	data, err := primary(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return os.ReadFile(path)
	}
	return data, err
}
