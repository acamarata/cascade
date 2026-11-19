// Package background provides keeper pass locking utilities.
package background

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RunKeeperPass executes a keeper pass protected by a lock file in stateDir.
// If another pass is already running (lock held by a live process), it prints
// a skip message to out and returns 0. A stale lock whose owner process is
// gone is taken over. The lock is always released via defer before returning.
// If run returns an error, RunKeeperPass returns 1; otherwise it returns 0.
func RunKeeperPass(stateDir string, out io.Writer, run func() error) int {
	lockPath := filepath.Join(stateDir, "keeper.lock")

	data, err := os.ReadFile(lockPath)
	if err == nil {
		s := strings.TrimSpace(string(data))
		if s != "" {
			pid, perr := strconv.Atoi(s)
			if perr == nil && pidAlive(pid) {
				fmt.Fprint(out, "keeper pass skip: another pass running\n")
				return 0
			}
		}
	}

	f, err := os.Create(lockPath)
	if err != nil {
		return 1
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()

	defer os.Remove(lockPath)

	if err := run(); err != nil {
		return 1
	}
	return 0
}
