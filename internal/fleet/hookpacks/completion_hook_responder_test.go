// Purpose: the raw unix-socket responder the completion hook command tests
// talk to: the system `nc` binary driven through a fifo, so the reply is
// written only after the first request byte has arrived.
// Constraints: os/exec only; this package never imports "net" (the listener
// gate in internal/build scans for it). A reply that is written the moment a
// client connects can reach Go's HTTP transport before it has sent its
// request ("Unsolicited response received on idle HTTP channel"), which made
// the hook exit 2 on a slow runner.
// SPORT: internal/fleet/hookpacks (P1-CI-20).
package hookpacks_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ncCloseFlag is "-N " when this nc closes the socket on stdin EOF (OpenBSD
// netcat), else "". macOS nc closes by itself and uses -N for something else.
var ncCloseFlag = sync.OnceValue(func() string {
	usage, _ := exec.Command("nc", "-h").CombinedOutput() // -h exits non-zero on some variants.
	return ncCloseFlagFrom(string(usage))
})

// ncCloseFlagFrom returns "-N " only when usage lists it as the EOF shutdown.
func ncCloseFlagFrom(usage string) string {
	for _, line := range strings.Split(usage, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[0] == "-N" && strings.Contains(line, "EOF") {
			return "-N "
		}
	}
	return ""
}

func TestNCCloseFlagFrom(t *testing.T) {
	for name, tc := range map[string]struct{ usage, want string }{
		"openbsd": {"\t-N\t\tShutdown the network socket after EOF on the input.\n", "-N "},
		"macos":   {"\t-N num_probes                 Number of probes to send before generating a write timeout event\n", ""},
		"none":    {"usage: nc [-46DdhklnrStUuvz]\n", ""},
	} {
		if got := ncCloseFlagFrom(tc.usage); got != tc.want {
			t.Errorf("%s: ncCloseFlagFrom = %q, want %q", name, got, tc.want)
		}
	}
}

// startFakeUnixResponder starts a background `nc -lU` process that binds
// sockPath immediately (verified by polling for the socket file) and, once
// the first request byte has arrived, writes resp and closes the connection.
// A reader keeps draining nc's stdout afterwards so a late request byte
// cannot SIGPIPE nc. Returns the bound socket path.
func startFakeUnixResponder(t *testing.T, resp string) string {
	t.Helper()
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available in this environment")
	}
	dir, err := os.MkdirTemp("", "s66t1")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath, respFile, fifo := filepath.Join(dir, "s.sock"), filepath.Join(dir, "resp.txt"), filepath.Join(dir, "reply.fifo")
	if err := os.WriteFile(respFile, []byte(resp), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"mkfifo %q && nc %s-lU %q <%q | { head -c 1 >/dev/null; cat %q; exec >&-; cat >/dev/null; } >%q",
		fifo, ncCloseFlag(), sockPath, fifo, respFile, fifo))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nc responder: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	waitForSocket(t, sockPath)
	return sockPath
}

// waitForSocket blocks until sockPath exists or the deadline passes.
func waitForSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nc responder never created socket %s", sockPath)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
