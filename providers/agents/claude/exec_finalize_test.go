//go:build !windows

package claude

// Purpose: a Cancel that arrives after the harness exited on its own (T0
//   decision F6): during a slow finalize the process group is already
//   confirmed empty, so Cancel sends no signal and never relabels the
//   finished run; it stays accepted.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   exec.go leaderExited/reaped. Finalize is slowed by a git wrapper put
//   first on PATH (the driver runs git from PATH); the wrapper marks when
//   finalize has started. No real harness runs.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeLateCancelKeepsFinishedRun(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f := newFx(t) // its worktree is made with the real git, before the wrapper
	wrap := t.TempDir()
	marker := filepath.Join(wrap, "finalize-started")
	gitWrapper := "#!/bin/sh\ntouch " + marker + "\nsleep 1\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrap, "git"), []byte(gitWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrap+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := f.driver(f.config())
	res, err := d.Spawn(context.Background(), f.spec())
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	eventually(t, "finalize to start", func() bool { return exists(marker) })
	start := time.Now()
	if err := d.Cancel(context.Background(), res.JobID); err != nil {
		t.Fatalf("a Cancel after the harness exited returned %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := d.Collect(ctx, res.JobID); err != nil || state(t, d, res.JobID) != provider.AgentRunAccepted {
		t.Fatalf("Collect = %v, state %s: a late Cancel relabelled a finished run", err, state(t, d, res.JobID))
	}
	if waited := time.Since(start); waited > d.cfg.Defaults.CancelGrace+cancelConfirmWindow {
		t.Fatalf("Cancel and Collect took %s", waited)
	}
}
