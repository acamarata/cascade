//go:build !windows

package claude

// Purpose: waitLeaderExited, so a test can land a second Cancel in the
//   window after the harness leader exited and before finalize, where a
//   pending Cancel must still wait for the job to read cancelled.
// SPORT: providers.agents.claude/EXTEND (P1-CI-28).

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func waitLeaderExited(t *testing.T, d *Driver, id provider.AgentJobID) {
	t.Helper()
	rec, err := d.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		rec.mu.Lock()
		exited := rec.leaderExited
		rec.mu.Unlock()
		if exited {
			return
		}
	}
	t.Fatal("the harness leader did not exit")
}
