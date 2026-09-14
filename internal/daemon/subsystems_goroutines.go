package daemon

// Purpose: the join point for the long-lived goroutines the Register*
//
//	methods start. Split out of subsystems.go to stay under Art.10.3's
//	300-line cap, the same rationale subsystems_worktree.go and
//	subsystems_scheduler.go were split on.
//
// Inputs: the function a subsystem wants to run in the background.
// Outputs: a goroutine the Manifest can wait for.
// Constraints: Wait joins, it does not cancel — a caller cancels the
//
//	subsystems' context first and then waits. Wait must therefore never be
//	called on a context that is still live, or it blocks for as long as the
//	subsystem runs, which is forever for a healthy daemon.
//
// SPORT: internal/daemon (ADD, subsystem goroutine join).

// goSubsystem runs fn in a tracked goroutine.
//
// Every long-lived subsystem goroutine goes through here so that Wait can
// join it. Firing them off untracked was a real defect and not only an
// untidiness: cancelling a context returns immediately while the goroutine
// is still mid-operation, so a caller that then removes the subsystem's
// working directory races it. On Unix that is invisible, because unlink
// succeeds on an open file; on Windows the open handle makes the removal
// fail outright, which is how this surfaced (a CI-only
// "The directory is not empty" on a test's own TempDir cleanup).
func (m *Manifest) goSubsystem(fn func()) {
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		fn()
	}()
}

// Wait blocks until every subsystem goroutine this Manifest started has
// returned. Cancel their context first — Wait does not cancel anything, it
// only joins.
func (m *Manifest) Wait() { m.running.Wait() }
