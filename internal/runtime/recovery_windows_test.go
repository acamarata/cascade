//go:build windows

// Purpose: Art.5 platform-parity proof for recovery.go's windows path —
//   asserts the platform-unsupported branch logs a WARN and returns
//   cleanly (nil, nil) without touching any filesystem path, rather than
//   merely asserting the package compiles under GOOS=windows. Split from
//   recovery_test.go under R-14.133 (a `//go:build windows` test file is
//   forced into existence exactly like that ruling's
//   `//go:build integration` case — a windows-only assertion cannot share
//   a file with the unix-oriented tests that dial real unix sockets and
//   spawn /bin/sh children, which do not exist on this platform).
// SPORT: runtime/recovery (ADD).

package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryScan_WindowsPlatformUnsupported(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(1, 0)),
		Log:         log,
	})
	if err != nil {
		t.Fatalf("Scan on windows: got error %v, want nil (clean return per HOW step 1)", err)
	}
	if ev != nil {
		t.Fatalf("Scan on windows: got event %+v, want nil", ev)
	}
	if !bytes.Contains(buf.Bytes(), []byte("platform-unsupported")) {
		t.Fatalf("expected a WARN log containing \"platform-unsupported\", got: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("level=WARN")) {
		t.Fatalf("expected the platform-unsupported log at WARN level, got: %s", buf.String())
	}
}
