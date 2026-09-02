package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Purpose: a size-based rotating io.Writer over a single log file, keyed
//   on Config.Logging.Rotation.MaxSizeMB/MaxFiles (T-2 contract task 2).
// Inputs: the log file path (LogFilePath, logger.go), a loggingRotation
//   (config.go — nil fields per R-14.107 mean "rotation stays disabled"),
//   and an injected Clock for the rotation-event line's timestamp.
// Outputs: an io.Writer (RotatingWriter) that appends to the active file
//   and, when rotation is enabled and a write would cross MaxSizeMB,
//   shifts numbered backups (path.1 newest .. path.MaxFiles oldest),
//   renames the active file to path.1 via os.Rename, opens a fresh active
//   file, and appends one JSON line naming the rotated path.
// Constraints: Art.7.1 — every caller in this package passes a
//   t.TempDir()-rooted path in tests. Art.7.3/02 §v1.1 — no bare
//   time.Now(); the rotation-event timestamp comes from the injected
//   Clock. File ops are serialised under one mutex so concurrent Write
//   calls never interleave into a torn line (-race requirement).
//   os.Rename is used for the atomic step — no CGO, no platform-specific
//   syscalls, so windows/amd64 (tier-2) builds and rotates the same way.
// SPORT: runtime/rotation (ADD, per T-2 sport_updates).

// RotatingWriter is an io.Writer over a single log file that optionally
// rotates by size. Reconfigure lets a running process change the
// thresholds without restarting (08 §3: [logging] is a fully
// hot-reloadable section).
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	maxSize  int64 // bytes; 0 disables rotation
	maxFiles int   // 0 disables rotation
	clock    Clock
}

// NewRotatingWriter opens (creating its directory as needed) the log file
// at path and returns a RotatingWriter configured from rotation.
// rotation.Enabled()==false (R-14.107: either key unset) leaves the
// writer rotating never — it appends to path forever. clock must not be
// nil in production (NewSystemClock()); tests inject a FixedClock so the
// rotation-event line's timestamp is deterministic (Art.7.3).
func NewRotatingWriter(path string, rotation loggingRotation, clock Clock) (*RotatingWriter, error) {
	if clock == nil {
		clock = NewSystemClock()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, &LogError{Field: "logging", Reason: fmt.Sprintf("create log directory for %s: %v", path, err)}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, &LogError{Field: "logging", Reason: fmt.Sprintf("open log file %s: %v", path, err)}
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, &LogError{Field: "logging", Reason: fmt.Sprintf("stat log file %s: %v", path, err)}
	}

	w := &RotatingWriter{path: path, file: f, size: info.Size(), clock: clock}
	w.applyRotation(rotation)
	return w, nil
}

// applyRotation sets maxSize/maxFiles from rotation, honouring R-14.107.
func (w *RotatingWriter) applyRotation(r loggingRotation) {
	if r.Enabled() {
		w.maxSize = int64(*r.MaxSizeMB) * 1024 * 1024
		w.maxFiles = *r.MaxFiles
		return
	}
	w.maxSize = 0
	w.maxFiles = 0
}

// Reconfigure updates rotation thresholds without restarting. Either
// argument <=0 disables rotation, matching R-14.107's "both or neither"
// semantics on the initial load.
func (w *RotatingWriter) Reconfigure(maxSizeMB, maxFiles int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if maxSizeMB > 0 && maxFiles > 0 {
		w.maxSize = int64(maxSizeMB) * 1024 * 1024
		w.maxFiles = maxFiles
		return
	}
	w.maxSize = 0
	w.maxFiles = 0
}

// Write implements io.Writer. It rotates first (when enabled and this
// write would cross MaxSizeMB) and then appends p to the active file. All
// file ops are serialised under w.mu.
//
// BLOCKING FIX 2 (CR on P1-E03-W1-S04-T2): a prior failed rotation could
// leave w.file nil (see rotateLocked/ensureFileLocked); this call
// lazily reopens w.path first so a transient rotation failure (disk
// full, permission hiccup) never permanently bricks the writer for the
// life of the process — without this, every later Write would fail
// forever, and since a slog handler swallows write errors, every
// subsequent log line would be silently dropped.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureFileLocked(); err != nil {
		return 0, err
	}

	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, &LogError{Field: "logging", Reason: fmt.Sprintf("write log file %s: %v", w.path, err)}
	}
	return n, nil
}

// ensureFileLocked reopens w.path in append mode if the writer has no
// usable open handle (w.file == nil), which happens only when a prior
// rotation closed the active file and then failed before a replacement
// handle was installed. Caller holds w.mu. A successful reopen also
// re-syncs w.size from the reopened file's actual length, since the
// in-memory size may predate whatever the failed rotation did or didn't
// do to the file on disk.
func (w *RotatingWriter) ensureFileLocked() error {
	if w.file != nil {
		return nil
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return &LogError{Field: "logging", Reason: fmt.Sprintf("reopen log file %s: %v", w.path, err)}
	}
	if info, statErr := f.Stat(); statErr == nil {
		w.size = info.Size()
	}
	w.file = f
	return nil
}

// osRename is a seam over os.Rename so tests can force the rename step
// of rotateLocked to fail deterministically, independent of the
// filesystem's privilege model. A chmod-based trigger (stripping write
// permission from the log directory) does not work under a root CI
// container — root ignores permission bits, so the rename silently
// succeeds and the failure path goes untested (BLOCKING FIX 3, CR on
// P1-E03-W1-S04-T2) — and is not portable to the windows/amd64 tier-2
// lane either. Overriding osRename works identically regardless of
// privilege level or platform. Production code always uses the real
// os.Rename; only rotation_test.go reassigns this var.
var osRename = os.Rename

// rotateLocked performs one size-triggered rotation. Caller holds w.mu.
// It prunes the backup that would overflow MaxFiles, shifts the
// remaining numbered backups up by one, renames the active file to
// path.1 (osRename — os.Rename in production, atomic on every platform
// this ticket targets), and opens a fresh active file carrying one JSON
// line naming the rotated path and its sequence number (always 1: the
// backup is, by construction, always the newest).
//
// BLOCKING FIX 2 (CR on P1-E03-W1-S04-T2): after w.file.Close(), any
// step below used to be able to fail while leaving w.file pointing at
// an already-closed handle, with no recovery — every later Write would
// fail forever until process restart, and a slog handler swallows write
// errors, so logging would go silently dark for the rest of the
// process's life over one transient hiccup (disk full, a permission
// blip). Both failure branches below now call recoverLocked, which
// reopens w.path — still holding the pre-rotation content, since
// neither shiftBackupsLocked nor a failed osRename ever moves the
// active file away — so the writer is left in a KNOWN-GOOD, still-
// usable state, and only the rotation itself is reported as failed.
func (w *RotatingWriter) rotateLocked() error {
	if err := w.file.Close(); err != nil {
		return &LogError{Field: "logging", Reason: fmt.Sprintf("close log file %s before rotation: %v", w.path, err)}
	}
	w.file = nil

	if w.maxFiles > 0 {
		if err := w.shiftBackupsLocked(); err != nil {
			return w.recoverLocked(err)
		}
	}

	rotated := w.numberedPath(1)
	if err := osRename(w.path, rotated); err != nil {
		return w.recoverLocked(&LogError{Field: "logging", Reason: fmt.Sprintf("rotate log file %s to %s: %v", w.path, rotated, err)})
	}

	if err := w.ensureFileLocked(); err != nil {
		return err
	}

	event := fmt.Sprintf("{\"time\":%q,\"level\":\"INFO\",\"msg\":\"log rotated\",\"rotated_path\":%q,\"sequence\":1}\n",
		w.clock.Now().UTC().Format(time.RFC3339Nano), rotated)
	if _, err := w.file.WriteString(event); err != nil {
		return &LogError{Field: "logging", Reason: fmt.Sprintf("write rotation event to %s: %v", w.path, err)}
	}
	w.size = int64(len(event))
	return nil
}

// recoverLocked reopens w.path after a rotation step failed before the
// active file's content was ever moved away from w.path (both call
// sites above only reach here in that situation), restoring a usable
// writer. It returns cause, the original rotation failure, unless the
// reopen itself also fails, in which case it wraps both into one typed
// error naming both failures.
func (w *RotatingWriter) recoverLocked(cause error) error {
	if err := w.ensureFileLocked(); err != nil {
		return &LogError{Field: "logging", Reason: fmt.Sprintf("%v; recovery also failed: %v", cause, err)}
	}
	return cause
}

// shiftBackupsLocked deletes the backup that would overflow maxFiles
// (path.maxFiles) and renames path.1..path.maxFiles-1 up to
// path.2..path.maxFiles, so path.1 is free for rotateLocked to claim.
func (w *RotatingWriter) shiftBackupsLocked() error {
	oldest := w.numberedPath(w.maxFiles)
	if _, err := os.Stat(oldest); err == nil {
		if err := os.Remove(oldest); err != nil {
			return &LogError{Field: "logging", Reason: fmt.Sprintf("prune rotated log %s: %v", oldest, err)}
		}
	}
	for n := w.maxFiles - 1; n >= 1; n-- {
		src := w.numberedPath(n)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, w.numberedPath(n+1)); err != nil {
			return &LogError{Field: "logging", Reason: fmt.Sprintf("shift rotated log %s: %v", src, err)}
		}
	}
	return nil
}

// numberedPath returns path's n'th rotated-backup name (path.1 is the
// newest backup, path.maxFiles the oldest kept).
func (w *RotatingWriter) numberedPath(n int) string {
	return fmt.Sprintf("%s.%d", w.path, n)
}

// Close flushes and closes the active log file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.file.Close(); err != nil {
		return &LogError{Field: "logging", Reason: fmt.Sprintf("close log file %s: %v", w.path, err)}
	}
	return nil
}
