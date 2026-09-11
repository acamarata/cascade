//go:build windows

// Purpose: the Windows mirror of lockfile_processalive_unix_test.go -
//   proves ProcessAlive's deliberate tier-2 stub (lockfile_windows.go)
//   actually refuses on this platform, for both a real live pid (this
//   process itself, so the test needs no child spawn at all) and the
//   invalid-pid case already covered cross-platform by
//   TestProcessAlive_InvalidPidIsUndecided.

package runtime

import (
	"errors"
	"os"
	"testing"
)

func TestProcessAlive_Windows_Unsupported(t *testing.T) {
	liveness, err := ProcessAlive(os.Getpid())
	if err == nil {
		t.Fatal("ProcessAlive on Windows: want the tier-2 unsupported error, got nil")
	}
	if !errors.Is(err, errProcessAliveUnsupportedWindows) {
		t.Fatalf("ProcessAlive on Windows: err = %v, want errProcessAliveUnsupportedWindows", err)
	}
	if liveness != ProcessLivenessUndecided {
		t.Fatalf("ProcessAlive on Windows: liveness = %v, want ProcessLivenessUndecided", liveness)
	}
}
