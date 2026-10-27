//go:build !windows

package claude

// Purpose: end every job a test left running before the test's t.TempDir is
//   removed, and prove the fixture does so.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; shared by
//   every _test.go here and split out for the 300-line cap. A Spawn returns
//   while the harness is still running and the supervisor still scans the
//   leased tree with git, so a test that never Collects or Cancels leaves a
//   writer inside its own TempDir and the RemoveAll cleanup races it
//   ("directory not empty"). Spawn staying asynchronous is the contract,
//   not a defect; the wait belongs to the caller, here the fixture.
// SPORT: providers.agents.claude tests (ADD).

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// settleBound is how long a test waits for one left-over job to finalize.
const settleBound = 20 * time.Second

// settleJobs cancels every job d still holds and waits for each to finalize
// with its process group confirmed empty (rec.done closes only then).
func settleJobs(t testing.TB, d *Driver) {
	t.Helper()
	d.mu.Lock()
	recs := make([]*jobRecord, 0, len(d.jobs))
	for _, rec := range d.jobs {
		recs = append(recs, rec)
	}
	d.mu.Unlock()
	for _, rec := range recs {
		ctx, cancel := context.WithTimeout(context.Background(), settleBound)
		_ = d.Cancel(ctx, rec.id) // a no-op for a job that already ended
		select {
		case <-rec.done:
		case <-ctx.Done():
			t.Errorf("job %s did not finalize within %s", rec.id, settleBound)
		}
		cancel()
	}
}

// A job a test abandons must be dead, and its writers gone, before the
// test's TempDir is removed. The harness keeps creating files in f.dir for
// seconds; the subtest returns at once, so only the fixture's cleanup (which
// runs before t.TempDir's RemoveAll) can stop it.
func TestFixtureSettlesAbandonedJobBeforeTempDirRemoval(t *testing.T) {
	var pid int
	var dir string
	t.Run("abandons a running job", func(t *testing.T) {
		f := newFx(t)
		dir = f.dir
		f.body = "echo $$ > " + filepath.Join(f.dir, "pid") + "\ni=0\nwhile [ $i -lt 600 ]; do : > " +
			filepath.Join(f.dir, "spew") + "$i; i=$((i+1)); sleep 0.01; done\n"
		if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		eventually(t, "the harness to start writing", func() bool { return exists(filepath.Join(f.dir, "spew0")) })
		pid = readPid(t, filepath.Join(f.dir, "pid"))
	})
	if pid <= 0 || dir == "" {
		t.Fatalf("the subtest did not record the harness (pid %d, dir %q)", pid, dir)
	}
	if alive(pid) {
		t.Fatalf("harness %d still running after its test ended: a writer outlives t.TempDir", pid)
	}
	if exists(dir) {
		t.Fatalf("%s survived the subtest's TempDir cleanup", dir)
	}
}
