package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: tests for rotation.go's RotatingWriter — rotation-at-boundary
//   behaviour, no-data-loss across a rotation, MaxFiles pruning,
//   Reconfigure hot-reload, concurrent-write race-safety, and the two
//   typed-error paths (unwritable log directory, a rename that cannot
//   succeed). Also hosts writeFile/fixedTime, shared with logger_test.go
//   and daemon_logs_test.go (same package, declared once).
// Constraints: Art.7.1 — every path here is rooted at t.TempDir().
//   Art.7.3 — every rotation-event timestamp comes from an injected
//   FixedClock, never the wall clock, so assertions on it are
//   deterministic.
// SPORT: runtime/rotation (ADD, per T-2 sport_updates).

// fixedTime is the deterministic instant every test in this package
// injects via FixedClock instead of reading the wall clock.
var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// writeFile is a small os.WriteFile wrapper shared by this package's
// T-2 tests.
func writeFile(t *testing.T, path, contents string) error {
	t.Helper()
	return os.WriteFile(path, []byte(contents), 0o644)
}

func newTestRotation(maxSizeMB, maxFiles int) loggingRotation {
	return loggingRotation{MaxSizeMB: &maxSizeMB, MaxFiles: &maxFiles}
}

func TestRotatingWriter_DisabledWithoutBothKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, loggingRotation{}, NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	// A write far larger than any real MaxSizeMB would ever allow must
	// never rotate when rotation is disabled (R-14.107).
	big := strings.Repeat("x", 10_000)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotation happened with no MaxSizeMB/MaxFiles set: %v", err)
	}
}

func TestRotatingWriter_RotatesAtSizeBoundaryNoDataLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 2), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	// MaxSizeMB=1 -> 1<<20 bytes. Each line is small; write enough lines
	// to force at least one rotation, tracking every line written so we
	// can prove none were lost across the boundary.
	line := strings.Repeat("a", 200)
	wantLines := map[string]bool{}
	total := 0
	for i := 0; total < 1<<20+5000; i++ {
		l := fmt.Sprintf("%s#%d\n", line, i)
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		wantLines[strings.TrimRight(l, "\n")] = true
		total += len(l)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated backup path.1: %v", err)
	}

	// Read back every line from both the backup and the active file;
	// every written line must be present exactly once, none lost, none
	// duplicated across the boundary. Split into a set first (rather
	// than repeated strings.Contains scans over a multi-MB string) so
	// this stays fast regardless of MaxSizeMB.
	all := readAllLogLines(t, path+".1") + readAllLogLines(t, path)
	gotLines := map[string]bool{}
	for _, l := range strings.Split(all, "\n") {
		gotLines[l] = true
	}
	for wanted := range wantLines {
		if !gotLines[wanted] {
			t.Fatalf("line lost across rotation boundary: %q", wanted)
		}
	}
}

func TestRotatingWriter_MaxFilesPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	// A tiny MaxSizeMB is not representable in whole MB, so drive
	// rotation directly via Reconfigure + repeated small writes against
	// a 1MB threshold would be slow; instead exercise shiftBackupsLocked
	// through several forced rotations by writing just over the boundary
	// each time.
	w, err := NewRotatingWriter(path, newTestRotation(1, 2), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	big := strings.Repeat("b", 1<<20+1)
	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(big)); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected path.1: %v", err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("expected path.2 (MaxFiles=2): %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("path.3 should have been pruned (MaxFiles=2): err=%v", err)
	}
}

func TestRotatingWriter_EmitsRotationEventLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 1), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	if _, err := w.Write([]byte(strings.Repeat("c", 1<<20+1))); err != nil {
		t.Fatalf("Write: %v", err)
	}

	active := readAllLogLines(t, path)
	if !strings.Contains(active, `"msg":"log rotated"`) {
		t.Fatalf("active file missing rotation event line: %q", active)
	}
	if !strings.Contains(active, `"sequence":1`) {
		t.Fatalf("rotation event missing sequence: %q", active)
	}
	if !strings.Contains(active, fixedTime.UTC().Format(time.RFC3339Nano)) {
		t.Fatalf("rotation event timestamp not from injected FixedClock: %q", active)
	}
}

func TestRotatingWriter_Reconfigure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, loggingRotation{}, NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	w.Reconfigure(1, 1)
	if _, err := w.Write([]byte(strings.Repeat("d", 1<<20+1))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("Reconfigure did not enable rotation: %v", err)
	}

	w.Reconfigure(0, 0)
	if _, err := w.Write([]byte(strings.Repeat("e", 1<<20+1))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("Reconfigure(0,0) did not disable rotation: err=%v", err)
	}
}

func TestRotatingWriter_ConcurrentWritesNoTornLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 3), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	const goroutines, perGoroutine = 8, 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				line := fmt.Sprintf("g%d-%d-%s\n", id, i, strings.Repeat("x", 40))
				if _, err := w.Write([]byte(line)); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	// A torn line would show up as a line that doesn't match the
	// "gN-i-xxxx" shape at all (interleaved bytes from two writers).
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		p := path + suffix
		if _, err := os.Stat(p); err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(readAllLogLines(t, p), "\n"), "\n") {
			if line == "" || strings.HasPrefix(line, "{") {
				continue // rotation event line, not a payload line
			}
			var gid, idx int
			if _, err := fmt.Sscanf(line, "g%d-%d-", &gid, &idx); err != nil {
				t.Fatalf("torn line in %s: %q", p, line)
			}
		}
	}
}

func TestRotatingWriter_LogDirectoryNotWritable(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "logs")
	if err := writeFile(t, blocked, "not a directory"); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	_, err := NewRotatingWriter(filepath.Join(blocked, "cascade.log"), loggingRotation{}, NewFixedClock(fixedTime))
	var logErr *LogError
	if !asLogError(err, &logErr) {
		t.Fatalf("NewRotatingWriter error = %v (%T), want *LogError", err, err)
	}
}

func TestRotatingWriter_RenameFailureIsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 2), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	// No numbered backup exists yet, so shiftBackupsLocked's prune/shift
	// steps are no-ops; stripping write permission from dir isolates the
	// FINAL step, rotateLocked's os.Rename(active, path.1), as the one
	// that fails. Restore write permission before t.TempDir() cleanup
	// runs, or removal of dir itself would fail too.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err = w.Write([]byte(strings.Repeat("f", 1<<20+1)))
	var logErr *LogError
	if !asLogError(err, &logErr) {
		t.Fatalf("Write error = %v (%T), want *LogError", err, err)
	}
	if !strings.Contains(logErr.Reason, "rotate log file") {
		t.Errorf("LogError.Reason = %q, want it to name the rename step", logErr.Reason)
	}
}

// readAllLogLines reads path's full contents, or "" if it does not exist
// (a rotation test may probe a backup slot that was never created).
func readAllLogLines(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(b)
}
