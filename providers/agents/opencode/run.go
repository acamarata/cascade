package opencode

// Purpose: bounded stdio decoding and a TERM/KILL cancellation ladder.
// Inputs: a child and its output filter. Outputs: confirmed terminal state.
// Constraints: no unfiltered bytes leave the job; exit precedes cancellation.

import (
	"bufio"
	"context"
	"io"
	"time"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

const maxEventBytes = 1 << 20

func (d *Driver) scan(j *job, r io.Reader, events bool) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), maxEventBytes)
	for s.Scan() {
		j.filterMu.Lock()
		b, err := j.spec.Filter(append([]byte(nil), s.Bytes()...))
		j.filterMu.Unlock()
		if err != nil {
			return provider.ErrEgressSubstitutionFailed
		}
		if events {
			if err := j.record(b); err != nil {
				return err
			}
		}
	}
	if s.Err() != nil {
		return ErrMalformedEvent
	}
	return nil
}

func (d *Driver) run(j *job) {
	stdoutDone := d.scanAsync(j, j.stdout, true)
	stderrDone := d.scanAsync(j, j.stderr, false)
	state, waitErr := j.process.Wait()
	confirmed := d.confirmGroup(j)
	err, stderrErr := d.drain(j, stdoutDone, stderrDone)
	_ = j.stdout.Close()
	_ = j.stderr.Close()
	d.finish(j, err, stderrErr, waitErr != nil || !state.Success(), confirmed)
}

func (d *Driver) scanAsync(j *job, r io.Reader, events bool) <-chan error {
	done := make(chan error, 1)
	go func() {
		err := d.scan(j, r, events)
		if err != nil {
			d.stop(j, err)
		}
		done <- err
	}()
	return done
}

func (d *Driver) drain(j *job, stdout, stderr <-chan error) (outErr, errErr error) {
	bound := time.NewTimer(time.Second)
	defer bound.Stop()
	for stdout != nil || stderr != nil {
		select {
		case outErr = <-stdout:
			stdout = nil
		case errErr = <-stderr:
			stderr = nil
		case <-bound.C:
			// A descendant outside the original group may still hold a pipe.
			// Close it and fail closed instead of blocking the lease forever.
			_ = j.stdout.Close()
			_ = j.stderr.Close()
			return ErrMalformedEvent, nil
		}
	}
	return outErr, errErr
}

func (d *Driver) finish(j *job, err, stderrErr error, failed, confirmed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	if j.err == nil {
		j.err = err
	}
	if j.err == nil {
		j.err = stderrErr
	}
	if j.err == nil && failed {
		j.err = ErrNonZeroExit
	}
	if j.err == nil && !j.terminal {
		j.err = ErrMissingTerminal
	}
	j.state = provider.AgentRunAccepted
	if j.err != nil {
		j.state = provider.AgentRunFailed
	}
	if j.err == ErrRunCancelled {
		j.state = provider.AgentRunCancelled
	}
	if !confirmed {
		j.state = provider.AgentRunCancelling
		j.err = provider.ErrCancelUnconfirmed
	}
	close(j.done)
}

// confirmGroup also reaps the launcher's residual group before admission ends.
func (d *Driver) confirmGroup(j *job) bool {
	if procgroup.Empty(j.pgid) {
		return true
	}
	_ = procgroup.Signal(j.pgid, procgroup.SigTerm)
	grace := time.NewTimer(d.cfg.Defaults.CancelGrace)
	defer grace.Stop()
	bound := time.NewTimer(d.cfg.Defaults.CancelGrace + 4*time.Second)
	defer bound.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if procgroup.Empty(j.pgid) {
				return true
			}
		case <-grace.C:
			_ = procgroup.Signal(j.pgid, procgroup.SigKill)
		case <-bound.C:
			return false
		}
	}
}

func (d *Driver) stop(j *job, cause error) {
	j.mu.Lock()
	if j.closed || j.stopping || j.state.Terminal() {
		j.mu.Unlock()
		return
	}
	j.stopping = true
	j.err = cause
	j.state = provider.AgentRunCancelling
	j.mu.Unlock()
	_ = procgroup.Signal(j.pgid, procgroup.SigTerm)
	go func() {
		t := time.NewTimer(d.cfg.Defaults.CancelGrace)
		defer t.Stop()
		select {
		case <-j.done:
			return
		case <-t.C:
			_ = procgroup.Signal(j.pgid, procgroup.SigKill)
		}
	}()
}

// Cancel signals the group and waits for process exit before reporting success.
func (d *Driver) Cancel(ctx context.Context, id provider.AgentJobID) error {
	j, err := d.lookup(id)
	if err != nil {
		return err
	}
	d.stop(j, ErrRunCancelled)
	t := time.NewTimer(d.cfg.Defaults.CancelGrace + 5*time.Second)
	defer t.Stop()
	select {
	case <-j.done:
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.state.Terminal() {
			return nil
		}
		return provider.ErrCancelUnconfirmed
	case <-ctx.Done():
	case <-t.C:
	}
	return provider.ErrCancelUnconfirmed
}
