package runtime

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: tests for rotation.go's rotation-failure recovery path
//   (BLOCKING FIX 2 and BLOCKING FIX 3, CR on P1-E03-W1-S04-T2) — split
//   out of rotation_test.go per R-14.117 (Art.10.3's 300-line file cap;
//   rotation_test.go crossed it once these two tests landed, and R-14.117
//   authorizes any ticket to split a file it owns into sibling files in
//   the same package to stay under the cap). Shares fixedTime,
//   newTestRotation, and readAllLogLines with rotation_test.go (same
//   package, declared once there).
// Constraints: Art.7.1 — every path here is rooted at t.TempDir().
// SPORT: runtime/rotation (ADD, per T-2 sport_updates).

// TestRotatingWriter_RenameFailureIsTypedError forces the rename step of
// rotateLocked to fail via the osRename seam (rotation.go), not
// filesystem permissions. BLOCKING FIX 3 (CR on P1-E03-W1-S04-T2): the
// previous version stripped write permission from the log directory
// (os.Chmod(dir, 0o555)), but root ignores permission bits — under a
// root CI container the chmod is a no-op as far as root's own access is
// concerned, so the rename SUCCEEDS, Write returns nil, and the test
// would t.Fatalf on a missing *LogError: a CI failure, not a graceful
// skip. It was also not portable to the windows/amd64 tier-2 lane
// (POSIX permission bits don't apply the same way). Overriding osRename
// forces the failure deterministically regardless of privilege level or
// platform, so this test is real evidence everywhere it runs — no skip
// needed.
func TestRotatingWriter_RenameFailureIsTypedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 2), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	injected := fmt.Errorf("injected rename failure")
	prevRename := osRename
	osRename = func(string, string) error { return injected }
	t.Cleanup(func() { osRename = prevRename })

	_, err = w.Write([]byte(strings.Repeat("f", 1<<20+1)))
	var logErr *LogError
	if !asLogError(err, &logErr) {
		t.Fatalf("Write error = %v (%T), want *LogError", err, err)
	}
	if !strings.Contains(logErr.Reason, "rotate log file") {
		t.Errorf("LogError.Reason = %q, want it to name the rename step", logErr.Reason)
	}
	if !strings.Contains(logErr.Reason, injected.Error()) {
		t.Errorf("LogError.Reason = %q, want it to wrap the injected cause", logErr.Reason)
	}
}

// TestRotatingWriter_UsableAfterFailedRotation proves BLOCKING FIX 2
// (CR on P1-E03-W1-S04-T2): before the fix, rotateLocked closed
// w.file and then, on any later step's failure (shiftBackupsLocked,
// osRename, or the reopen), left w.file pointing at that already-closed
// handle with no recovery — every subsequent Write failed forever until
// process restart, and since a slog handler swallows write errors,
// every later log line was silently dropped. This asserts the writer
// stays usable: a Write right after a failed rotation must still
// succeed and land in the file.
func TestRotatingWriter_UsableAfterFailedRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	w, err := NewRotatingWriter(path, newTestRotation(1, 2), NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	prevRename := osRename
	osRename = func(string, string) error { return fmt.Errorf("injected rename failure") }
	if _, err := w.Write([]byte(strings.Repeat("g", 1<<20+1))); err == nil {
		osRename = prevRename
		t.Fatal("Write with forced rename failure returned nil error, want the injected failure")
	}
	osRename = prevRename

	if _, err := w.Write([]byte("still usable\n")); err != nil {
		t.Fatalf("Write after failed rotation: %v, want the writer to have recovered", err)
	}
	if !strings.Contains(readAllLogLines(t, path), "still usable") {
		t.Fatalf("recovered write did not land in %s", path)
	}
}
