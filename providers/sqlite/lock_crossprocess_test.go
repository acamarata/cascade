// Purpose: the cross-process half of the §D-3 arbitration tests, split out
//
//	of lock_test.go under Art.10.3's 300-line file cap. TestHelperProcess
//	is re-executed as a SEPARATE OS PROCESS (never called directly) so
//	TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess proves a
//	genuine cross-process invariant rather than same-process reentrancy —
//	see that test's doc comment for why same-process exclusion is a
//	strictly weaker claim, and this is the test written to fail on trap 1
//	(a zero-length windows lock range) before the first push.
//
// SPORT: providers.sqlite.Driver/ADDED (windows LockFileEx ticket).
package sqlite_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// crossProcessHelperEnv is the env var
// TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess sets to the
// database path when it re-execs this test binary as TestHelperProcess.
// Its presence signals "act as the lock holder", never a real test run.
const crossProcessHelperEnv = "CASCADE_SQLITE_LOCKTEST_HELPER_PATH"

// TestHelperProcess is re-executed as a SEPARATE OS PROCESS by
// TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess (via
// exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")) rather than
// called directly, so the exclusion it proves is a genuine cross-process
// invariant. A same-process second sqlite.Open call is a strictly weaker
// claim: on windows, a zero-length LockFileEx range (this ticket's trap 1)
// would make a same-process double-open ALSO look excluded for the wrong
// reason, so only a distinct OS process re-attempting Open can catch it.
// Invoked as an ordinary `go test` run (crossProcessHelperEnv unset) this
// is a no-op.
func TestHelperProcess(_ *testing.T) {
	path := os.Getenv(crossProcessHelperEnv)
	if path == "" {
		return
	}
	d, err := sqlite.Open(context.Background(), path)
	if err != nil {
		fmt.Println("OPEN_FAILED")
		return
	}
	fmt.Println("OPEN_OK")
	// Block until the parent writes a byte, so the parent's own contention
	// Open genuinely overlaps this process holding the lock rather than
	// racing a fixed sleep.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	_ = d.Close()
}

// TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess proves the §D-3
// "never two writers" invariant against a REAL second OS process. This is
// the test written to fail on trap 1 (a zero-length windows lock range):
// with a zero-length range the helper process's LockFileEx call would
// still "succeed" and this process's own Open would ALSO succeed, and the
// assertion below fails loudly rather than passing on a lock that locks
// nothing.
func TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), crossProcessHelperEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	line, scanErr := readHelperSentinel(stdout)
	if line != "OPEN_OK" {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("helper process: want OPEN_OK, got %q (scanErr=%v)", line, scanErr)
	}

	// The helper process now holds the §D-3 lock. A second Open from THIS
	// process — a genuinely different process than the helper — must be
	// refused.
	_, openErr := sqlite.Open(context.Background(), path)

	// Release the helper regardless of outcome so it never leaks.
	_, _ = stdin.Write([]byte{'\n'})
	_ = stdin.Close()
	if waitErr := cmd.Wait(); waitErr != nil {
		t.Errorf("helper process exit: %v", waitErr)
	}

	assertCrossProcessRefusal(t, openErr)

	// After the helper releases, a fresh Open must succeed — proves the
	// helper's own unlock path (UnlockFileEx before CloseHandle on
	// windows, LOCK_UN before Close on unix) actually released the lock
	// rather than leaking it.
	after, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open after helper released: %v", err)
	}
	if err := after.Close(); err != nil {
		t.Errorf("after.Close: %v", err)
	}
}

// readHelperSentinel scans r for TestHelperProcess's "OPEN_OK" or
// "OPEN_FAILED" line, skipping anything else the subprocess may write
// first.
func readHelperSentinel(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "OPEN_OK" || line == "OPEN_FAILED" {
			return line, nil
		}
	}
	return "", scanner.Err()
}

// assertCrossProcessRefusal is split out of
// TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess to keep that test
// under Art.10.3's 50-line function cap.
func assertCrossProcessRefusal(t *testing.T, openErr error) {
	t.Helper()
	if openErr == nil {
		t.Fatal("second Open from a distinct process: want a §D-3 refusal, got nil error — this is exactly trap 1 (a lock that locks nothing)")
	}
	if !cascade.HasKind(openErr, cascade.KindConflict) {
		t.Fatalf("second Open from a distinct process: want KindConflict, got %v", openErr)
	}
	if !errors.Is(openErr, sqlite.ErrLockHeld) {
		t.Fatalf("second Open from a distinct process: want errors.Is(err, sqlite.ErrLockHeld), got %v", openErr)
	}
}
