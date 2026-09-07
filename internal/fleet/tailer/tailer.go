// Package tailer streams redacted transcript records from the on-disk
// JSONL transcripts the cc and codex coding harnesses write during a
// session, following each file safely across rotation (rename+reopen) and
// truncation. See docs/adrs/ADR-E09T6-harness-transcript-stability.md for
// the field-stability evidence this package's dispatch logic is built on,
// and redact.go for the R-21.152 first-boundary redaction every Record
// passes through before a caller ever sees it.
//
// opencode is deliberately out of scope: per the ADR, it writes no
// transcript file at all (its session state lives in a SQLite database),
// so a file tailer cannot serve it. That harness gets a database-reading
// implementation behind a shared source interface, carried by a later
// ticket, not a Tailer.
package tailer

import (
	"bytes"
	"io"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: rotation-safe line follower over one harness transcript file.
// Inputs: a file path, the Harness that writes it, and the worktree root
//   redact.go scopes file-path fields against.
// Outputs: Next() yields one redacted Record per transcript line, or
//   io.EOF when the tailer has caught up with the file's current content
//   (not a permanent stop — callers poll and call Next again).
// Constraints: no platform-specific build tags — rotation and truncation
//   detection use only os.SameFile and os.FileInfo.Size, both portable,
//   so the GOOS matrix (darwin/linux/windows) builds identically. Every
//   read is buffered in-process (t.pending); an unterminated final line
//   is never treated as complete, so a line split across two polls is
//   never truncated or duplicated.
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

// readChunkSize is how much the tailer reads from the file per poll when
// pending has no complete line buffered yet.
const readChunkSize = 64 * 1024

// Tailer follows one harness transcript file and yields redacted Records.
// It is not safe for concurrent use by multiple goroutines; callers (the
// sessions domain's poll loop) drive Next() from a single goroutine per
// Tailer.
type Tailer struct {
	path     string
	harness  Harness
	worktree string
	parser   *parser

	file    *os.File
	info    os.FileInfo
	offset  int64
	lineNo  int
	pending []byte
}

// NewTailer opens path and returns a Tailer that decodes it as harness's
// transcript format, scoping any file-path fields it emits against
// worktreeRoot (see redact.go — a path outside worktreeRoot is dropped,
// fail-closed).
func NewTailer(path string, harness Harness, worktreeRoot string) (*Tailer, error) {
	t := &Tailer{path: path, harness: harness, worktree: worktreeRoot, parser: newParser(harness)}
	if err := t.open(); err != nil {
		return nil, err
	}
	return t, nil
}

// Close releases the tailer's open file handle.
func (t *Tailer) Close() error {
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

func (t *Tailer) open() error {
	f, err := os.Open(t.path)
	if err != nil {
		return cascade.Wrapf(cascade.KindNotFound, err, "open transcript %s", t.path)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return cascade.Wrapf(cascade.KindInternal, err, "stat transcript %s", t.path)
	}
	t.file = f
	t.info = info
	t.offset = 0
	t.lineNo = 0
	t.pending = nil
	t.parser.reset()
	return nil
}

// Next returns the next redacted Record decoded from the transcript, or
// io.EOF when the tailer has caught up with the file's current content.
// io.EOF is not a permanent stop: the caller's poll loop calls Next again
// later, and rotation or truncation that has happened in the meantime is
// picked up transparently on that next call. A malformed or
// unrecognised-version line surfaces as *ParseError (never a panic and
// never a silently dropped record); the caller decides whether to keep
// polling past it.
func (t *Tailer) Next() (Record, error) {
	for {
		if idx := bytes.IndexByte(t.pending, '\n'); idx >= 0 {
			line := t.pending[:idx]
			t.pending = append([]byte(nil), t.pending[idx+1:]...)
			t.lineNo++
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			ev, perr := t.parser.parseLine(line, t.lineNo)
			if perr != nil {
				return Record{}, perr
			}
			return redact(ev, t.worktree), nil
		}

		n, err := t.fill()
		if n > 0 {
			continue
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return Record{}, cascade.Wrapf(cascade.KindInternal, err, "read transcript %s", t.path)
		}

		rotated, rerr := t.checkRotation()
		if rerr != nil {
			return Record{}, rerr
		}
		if rotated {
			continue
		}
		return Record{}, io.EOF
	}
}

// fill reads one chunk from the open file into t.pending, advancing
// t.offset by however many bytes were actually read.
func (t *Tailer) fill() (int, error) {
	buf := make([]byte, readChunkSize)
	n, err := t.file.Read(buf)
	if n > 0 {
		t.pending = append(t.pending, buf[:n]...)
		t.offset += int64(n)
	}
	return n, err
}

// checkRotation stats t.path and compares it against the currently open
// file's identity (os.SameFile — portable across unix inode and Windows
// file-index semantics, no build tags needed). A different file at the
// same path means a rename-then-reopen rotation: it is reopened from
// offset 0. The same file with a size smaller than the tailer's current
// offset means an in-place truncation: the tailer seeks back to 0. Either
// case returns rotated=true so Next's loop retries the read; a path stat
// failure (e.g. the file briefly does not exist between rename steps) is
// treated as "not yet rotated" rather than an error, since the caller
// polls again regardless.
func (t *Tailer) checkRotation() (rotated bool, err error) {
	stat, statErr := os.Stat(t.path)
	if statErr != nil {
		return false, nil
	}
	if !os.SameFile(t.info, stat) {
		if reopenErr := t.open(); reopenErr != nil {
			return false, reopenErr
		}
		return true, nil
	}
	if stat.Size() < t.offset {
		if _, seekErr := t.file.Seek(0, io.SeekStart); seekErr != nil {
			return false, cascade.Wrapf(cascade.KindInternal, seekErr, "seek transcript %s after truncation", t.path)
		}
		t.info = stat
		t.offset = 0
		t.lineNo = 0
		t.pending = nil
		t.parser.reset()
		return true, nil
	}
	return false, nil
}
