// Purpose: the job child's lifetime: start in its own process group,
//   stdout/stderr pumps through the OutputFilter, the leader-exit reap of
//   the whole group, and Cancel with the R-21.174/R-21.140
//   ladder (SIGTERM to the group, CancelGrace, SIGKILL to the group, then a
//   confirmed-empty group before cancelled is ever reported). Message and
//   Status live in collect.go.
// Inputs: a spawnPlan every guard has passed.
// Outputs: a registered jobRecord and its supervisor goroutine.
// Constraints: the child's lifetime is driver-owned (exec.Command, never
//   the Spawn context). A failed process-group lookup refuses the spawn and
//   kills the child. Status follows R-14.316: running, cancelling,
//   cancelled, failed, or accepted for a clean exit. No signal is sent once
//   the group is confirmed empty, and a Cancel after the harness exited
//   never relabels the run. Cancel returns within CancelGrace plus
//   cancelConfirmWindow.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	cancelConfirmWindow = 5 * time.Second
	stragglerSettle     = 500 * time.Millisecond
	stragglerGrace      = 2 * time.Second
	pumpDrainBound      = 2 * time.Second
)

// jobRecord is one spawned job's live state.
type jobRecord struct {
	id         provider.AgentJobID
	pgid       int
	proc       *os.Process
	lease      Lease
	vendorVars []string
	protocol   provider.ProtocolVersion
	dataClass  provider.DataClass
	injected   map[string]bool
	stdout     *filteredWriter
	stderr     *filteredWriter
	scanOut    *canaryScanner
	scanErr    *canaryScanner
	channel    *callbackChannel
	canary     *canary

	mu              sync.Mutex
	state           provider.AgentRunState
	cancelRequested bool
	laddering       bool
	leaderExited    bool // the harness exited: a later Cancel relabels nothing
	reaped          bool // the group is confirmed empty: no more signals
	egressFailed    bool
	leak            bool
	scanFailed      bool // the tree scan itself failed closed after a failing exit
	exitCode        int
	outcome         provider.CollectResult
	outcomeErr      error
	done            chan struct{} // closed after finalize, group empty
}

// startChild execs the job in its own group and starts its supervisor; a
// failure undoes the per-spawn state first.
func (d *Driver) startChild(p *spawnPlan) (*jobRecord, error) {
	rec, err := d.newRecord(p)
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		rec.release()
		return nil, cascade.Wrapf(cascade.KindInternal, err, "claude: stdout pipe")
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(outR, outW)
		rec.release()
		return nil, cascade.Wrapf(cascade.KindInternal, err, "claude: stderr pipe")
	}
	cmd := exec.Command(d.cfg.Binary, p.argv...)
	// cwd is spec.WorktreeRoot (prepare proved it equals the lease root).
	cmd.Dir, cmd.Env, cmd.Stdin = p.job.WorktreeRoot, p.env, nil // nil Stdin is the null device
	cmd.Stdout, cmd.Stderr = outW, errW
	setProcessGroup(cmd)
	startErr := cmd.Start()
	closeAll(outW, errW)
	if startErr != nil {
		closeAll(outR, errR)
		rec.release()
		return nil, cascade.Wrapf(cascade.KindUnavailable, startErr, "claude: starting the harness")
	}
	if rec.pgid, err = resolvePgid(cmd.Process.Pid); err != nil {
		_ = signalGroup(cmd.Process.Pid, procgroup.SigKill)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		closeAll(outR, errR)
		rec.release()
		return nil, err
	}
	rec.proc = cmd.Process
	rec.channel.setFailHook(func() { rec.startLadder(d.cfg.Defaults.CancelGrace) })
	d.register(rec)
	go d.supervise(rec, outR, errR)
	return rec, nil
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close() // a second close of the same pipe end is harmless
	}
}

// supervise owns the job after start: pumps, leader reap, group reap,
// finalize, cleanup; done closes only after the group is confirmed empty.
func (d *Driver) supervise(rec *jobRecord, outR, errR *os.File) {
	var pumps sync.WaitGroup
	pumps.Add(2)
	go rec.pump(d, outR, rec.stdout, rec.scanOut, &pumps)
	go rec.pump(d, errR, rec.stderr, rec.scanErr, &pumps)
	st, err := rec.proc.Wait()
	rec.mu.Lock()
	rec.exitCode, rec.leaderExited = -1, true
	if err == nil && st != nil {
		rec.exitCode = st.ExitCode()
	}
	rec.mu.Unlock()
	d.reapAfterLeader(rec)
	rec.mu.Lock()
	rec.reaped = true
	rec.mu.Unlock()
	drainPumps(&pumps, outR, errR)
	rec.channel.close() // no frame is accepted after the verdict starts
	d.finalize(rec)
	rec.release()
	close(rec.done)
}

// pump copies one stream through its filteredWriter, scanning the raw bytes
// for the canary first. A filter error or an overflow stops the child
// through the ladder (fail closed); the pipe keeps draining meanwhile.
func (r *jobRecord) pump(d *Driver, src *os.File, w *filteredWriter, scan *canaryScanner, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			scan.Scan(buf[:n])
			if werr := w.Write(buf[:n]); werr != nil {
				r.failStream(d, errors.Is(werr, errFilterFailed))
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *jobRecord) failStream(d *Driver, egress bool) {
	if egress {
		r.mu.Lock()
		r.egressFailed = true
		r.mu.Unlock()
	}
	r.startLadder(d.cfg.Defaults.CancelGrace)
}

// startLadder runs terminate once per job on a driver-owned context: a
// caller whose context ends early gets ErrCancelUnconfirmed, but the ladder
// still reaches SIGKILL and the confirmed-empty group.
func (r *jobRecord) startLadder(grace time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.laddering || r.reaped {
		return
	}
	r.laddering = true
	go terminate(context.Background(), r.pgid, grace)
}

// drainPumps waits for both pumps; a member that escaped the group can hold
// a pipe open, so after pumpDrainBound the read ends are closed.
func drainPumps(pumps *sync.WaitGroup, outR, errR *os.File) {
	finished := make(chan struct{})
	go func() { pumps.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(pumpDrainBound):
		closeAll(outR, errR)
		<-finished
	}
	closeAll(outR, errR)
}

// reapAfterLeader ends every member the leader left behind. A straggler
// carrying the vendor auth variable (or whose environment cannot be read)
// marks the run as leaked before the group is terminated.
func (d *Driver) reapAfterLeader(rec *jobRecord) {
	settle, cancel := context.WithTimeout(context.Background(), stragglerSettle)
	empty := waitGroupEmpty(settle, rec.pgid)
	cancel()
	if empty {
		return
	}
	if auditStragglers(rec.pgid, rec.vendorVars) {
		rec.mu.Lock()
		rec.leak = true
		rec.mu.Unlock()
	}
	terminate(context.Background(), rec.pgid, min(d.cfg.Defaults.CancelGrace, stragglerGrace))
}

// terminate signals SIGTERM to the group, waits grace for it to empty, then
// signals SIGKILL to the group (even when the leader is already gone) and
// waits for a confirmed-empty group until ctx ends.
func terminate(ctx context.Context, pgid int, grace time.Duration) bool {
	_ = signalGroup(pgid, procgroup.SigTerm)
	g, cancel := context.WithTimeout(ctx, grace)
	ok := waitGroupEmpty(g, pgid)
	cancel()
	if ok {
		return true
	}
	_ = signalGroup(pgid, procgroup.SigKill)
	return waitGroupEmpty(ctx, pgid)
}

// waitGroupEmpty polls until the group is confirmed empty (true) or ctx
// ends (false). A transient probe error or a zombie-only group is retried,
// never reported as empty or as a failure.
func waitGroupEmpty(ctx context.Context, pgid int) bool {
	wait := 5 * time.Millisecond
	for {
		if groupIsEmpty(pgid) {
			return true
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
		wait = min(wait*2, 100*time.Millisecond)
	}
}

// Cancel starts the ladder and waits for the job to finalize. It returns
// nil once the group is confirmed empty and the job reads cancelled (or it
// was already terminal or its harness had already exited, which it leaves
// to finish unrelabelled), and ErrCancelUnconfirmed when ctx or the bound
// CancelGrace+cancelConfirmWindow ends first, leaving the job cancelling
// while the ladder carries on; a later Cancel waits on the same ladder.
func (d *Driver) Cancel(ctx context.Context, id provider.AgentJobID) error {
	rec, err := d.lookup(id)
	if err != nil {
		return err
	}
	if !rec.beginCancel() {
		return nil
	}
	grace := d.cfg.Defaults.CancelGrace
	rec.startLadder(grace)
	bound := time.NewTimer(grace + cancelConfirmWindow)
	defer bound.Stop()
	select {
	case <-rec.done:
		return nil
	case <-ctx.Done():
	case <-bound.C:
	}
	return provider.ErrCancelUnconfirmed
}

func (r *jobRecord) beginCancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Terminal() || r.leaderExited {
		return false
	}
	r.cancelRequested = true
	r.state = provider.AgentRunCancelling
	return true
}
