// Purpose: the platform-neutral ordering core of the windows step reap.
// A windows step starts suspended, is assigned to a Job Object, and only
// then resumed; on the deadline the job is terminated and every member is
// waited for before Run returns. The ORDER of those moves is the whole
// correctness argument, and it lives here, untagged, behind the jobOps
// seam, so darwin and linux test and mutate it with a fake while the
// windows file supplies only the Win32 calls (runner_exec_windows.go).
//
// Inputs: a jobOps (the real Win32 binding on windows, a recording fake in
// tests) and the step's *os.Process.
// Outputs: typed errors only. Every failure is KindUnavailable.
// Constraints: fail closed. A job without its kill-on-close limit never
// runs a step; a child that could not be assigned or resumed is killed
// while still suspended; a child killed before attach is never resumed;
// all state is read and written under mu, which attach holds across
// assign+resume so kill can never interleave with them.
// SPORT: internal.ci.jobTree/ADDED, internal.ci.waitTreeGone/ADDED.

package ci

import (
	"errors"
	"os"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// waitTreeGone calls round until it reports zero members. Each round
// terminates what it found and waits for it, so there is no pause and no
// poll between rounds. attempts bounds the rounds; a tree still reporting
// members after the last one is an error, never a silent success.
func waitTreeGone(round func() (int, error), attempts int) error {
	for i := 0; i < attempts; i++ {
		n, err := round()
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "ci: reaping the timed-out step's process tree")
		}
		if n == 0 {
			return nil
		}
	}
	return cascade.New(cascade.KindUnavailable, "ci: the timed-out step's process tree is still running")
}

// jobOps is the platform binding a jobTree drives. On windows it is winJob
// (a Job Object); tests pass a fake that records the call order.
type jobOps interface {
	// limitKillOnClose makes closing the job kill every member.
	limitKillOnClose() error
	// assign puts the (still suspended) process into the job.
	assign(pid int) error
	// resume resumes the process's single suspended thread.
	resume(pid int) error
	// killChild kills the step's own process only.
	killChild(p *os.Process) error
	// openMembers opens a wait handle on every live member and returns
	// how many it opened.
	openMembers() (int, error)
	// terminate kills every member of the job.
	terminate() error
	// waitMembers waits on, then closes, the handles openMembers opened.
	waitMembers() error
	// close releases the job; with the limit set, that kills any member.
	close() error
}

// jobTree orders attach and kill for one step. It is safe for the
// concurrent attach (the Run goroutine) and kill (os/exec's context
// watcher) that exec.CommandContext produces.
type jobTree struct {
	mu       sync.Mutex
	ops      jobOps
	attached bool
	killed   bool
}

// newJobTree sets the kill-on-close limit before any step can start. A
// job that refuses the limit is closed and the step never starts.
func newJobTree(ops jobOps) (*jobTree, error) {
	if err := ops.limitKillOnClose(); err != nil {
		_ = ops.close()
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: limiting the step's job to kill on close")
	}
	return &jobTree{ops: ops}, nil
}

// attach assigns the suspended child to the job and only then resumes it.
// A child already killed is left alone (never resumed). Any failure kills
// the child while it is still suspended and returns KindUnavailable.
func (t *jobTree) attach(p *os.Process) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.killed {
		return nil
	}
	if err := t.ops.assign(p.Pid); err != nil {
		return t.refuse(p, err, "ci: assigning the step to its job object")
	}
	if err := t.ops.resume(p.Pid); err != nil {
		return t.refuse(p, err, "ci: resuming the step's suspended shell")
	}
	t.attached = true
	return nil
}

// refuse kills the unconfined child and returns the attach failure. The
// caller holds mu.
func (t *jobTree) refuse(p *os.Process, cause error, msg string) error {
	if kerr := t.ops.killChild(p); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
		cause = errors.Join(cause, kerr)
	}
	return cascade.Wrap(cascade.KindUnavailable, cause, msg)
}

// kill reaps the step. Before attach the child is still suspended and has
// no descendants, so killing it is the whole reap. After attach the job
// is terminated and every member waited for, round by round.
func (t *jobTree) kill(p *os.Process, attempts int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.killed = true
	if !t.attached {
		if err := t.ops.killChild(p); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return cascade.Wrap(cascade.KindUnavailable, err, "ci: killing the step's suspended shell")
		}
		return nil
	}
	return waitTreeGone(t.round, attempts)
}

// round is one reap pass: open the members, terminate the job, wait for
// every opened member. It returns how many members it opened. The caller
// holds mu.
func (t *jobTree) round() (int, error) {
	n, err := t.ops.openMembers()
	if err != nil || n == 0 {
		return 0, err
	}
	if err := t.ops.terminate(); err != nil {
		return n, err
	}
	if err := t.ops.waitMembers(); err != nil {
		return n, err
	}
	return n, nil
}

// close releases the job under mu, so it can never interleave with kill.
func (t *jobTree) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ops.close()
}
