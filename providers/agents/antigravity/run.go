package antigravity

// Purpose: run one agy print job: spawn it in its own process group,
// stream its decoded stream-json output, bound it by --print-timeout and
// cancel it with a confirmed exit.
// Inputs: a host-validated job specification and lifecycle requests.
// Outputs: a live job id and process group, an ordered event log tagged
// with the job's joined data class, and the filtered result.
// Constraints: the job's tier is the join of the spec's data class and the
// envelope sensitivity; a failed job withholds its output; the backstop
// timer kills the whole group when the print timeout passes.

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrPrintTimeout ends a job whose print process outlived its print timeout.
var ErrPrintTimeout = cascade.New(cascade.KindTimeout, "antigravity: print process exceeded its print timeout")

type job struct {
	cmd    *exec.Cmd
	pgid   int
	done   chan struct{}
	timer  *time.Timer
	tier   provider.DataClass
	seq    uint64
	events []provider.AgentEvent
	state  provider.AgentRunState
	result provider.CollectResult
	err    error
}

// Spawn validates custody before starting one print process in its own group.
func (d *Driver) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	if err := d.preflight(spec); err != nil {
		return provider.SpawnResult{}, err
	}
	remaining := spec.Envelope.ExpiresAt.Sub(d.cfg.Clock.Now())
	argv, err := BuildArgv(d.cfg.Binary, d.cfg.Defaults, spec, remaining)
	if err != nil {
		return provider.SpawnResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.SpawnResult{}, ErrRunCancelled
	}
	id := spec.JobID
	if id == "" {
		id = newJobID()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if old := d.jobs[id]; old != nil && !old.state.Terminal() {
		return provider.SpawnResult{}, ErrDuplicateJob
	}
	j, stdout, err := start(argv, spec)
	if err != nil {
		return provider.SpawnResult{}, err
	}
	j.tier = provider.JoinSensitivity(spec.DataClass, spec.Envelope.Sensitivity)
	j.result = provider.CollectResult{JobID: id, DataClass: j.tier, Artifacts: []string{}}
	d.jobs[id] = j
	j.timer = time.AfterFunc(printTimeout(d.cfg.Defaults, remaining), func() { d.expire(j) })
	go d.readJob(j, bufio.NewScanner(stdout), spec.Filter)
	return provider.SpawnResult{JobID: id, ProcessGroupID: j.pgid}, nil
}

// start launches argv with the spec's exact environment in a new group.
func start(argv []string, spec provider.AgentJobSpec) (*job, io.Reader, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.SysProcAttr = append([]string(nil), spec.Env.Driver...), spec.WorktreeRoot, procgroup.Attr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, ErrStart
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, ErrStart
	}
	pgid, err := procgroup.Lookup(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, ErrStart
	}
	return &job{cmd: cmd, pgid: pgid, done: make(chan struct{}), state: provider.AgentRunRunning}, stdout, nil
}

// printTimeout mirrors the --print-timeout value BuildArgv emits.
func printTimeout(defaults provider.DriverDefaults, remaining time.Duration) time.Duration {
	timeout := defaults.ApprovalTimeout
	if timeout == 0 {
		timeout = defaultPrintTimeout
	}
	return min(timeout, remaining)
}

// expire kills a job that is still running when its print timeout passes.
func (d *Driver) expire(j *job) {
	d.mu.Lock()
	running := j.state == provider.AgentRunRunning
	if running {
		j.err = ErrPrintTimeout
	}
	d.mu.Unlock()
	if running {
		_ = procgroup.Signal(j.pgid, procgroup.SigKill)
	}
}

func (d *Driver) readJob(j *job, scanner *bufio.Scanner, filter provider.OutputFilter) {
	decoder, _ := NewDecoder(filter)
	failure := d.drain(j, decoder, scanner)
	if err := j.cmd.Wait(); err != nil && failure == nil {
		failure = ErrNonZeroExit
	}
	j.timer.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	if j.err == nil {
		j.err = failure
	}
	j.result.Output = decoder.Output()
	end := provider.DriverEvent{Kind: provider.DriverEventDone, State: provider.AgentRunAccepted}
	switch j.err {
	case nil:
	case ErrRunCancelled:
		end = provider.DriverEvent{Kind: provider.DriverEventError, State: provider.AgentRunCancelled, Err: j.err}
	default:
		end = provider.DriverEvent{Kind: provider.DriverEventError, State: provider.AgentRunFailed, Err: j.err}
	}
	j.state = end.State
	j.recordLocked(end)
	close(j.done)
}

// drain decodes stdout line by line, recording every decoded event; a
// decode failure kills the group and is returned.
func (d *Driver) drain(j *job, decoder *Decoder, scanner *bufio.Scanner) error {
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		ev, err := decoder.Decode(scanner.Bytes())
		if err != nil {
			_ = procgroup.Signal(j.pgid, procgroup.SigKill)
			return err
		}
		d.mu.Lock()
		j.recordLocked(provider.DriverEvent{Kind: provider.DriverEventStdout, Text: ev.Text,
			Native: &provider.NativeEvent{Type: ev.Type}})
		d.mu.Unlock()
	}
	if scanner.Err() != nil {
		return ErrMalformedEvent
	}
	return nil
}

// recordLocked tags ev with the next Seq and the job's joined tier (the
// DriverEvent zero class is restricted, so the tier is set, not joined), then
// appends its normalized form. The caller holds the driver lock.
func (j *job) recordLocked(ev provider.DriverEvent) {
	j.seq++
	ev.Seq, ev.DataClass = j.seq, j.tier
	if out, err := provider.NormalizeEvent(ev); err == nil {
		out.Protocol = ProtocolRevision
		j.events = append(j.events, out)
	}
}

// Events returns a copy of the job's ordered event log so far.
func (d *Driver) Events(id provider.AgentJobID) ([]provider.AgentEvent, error) {
	j, err := d.lookup(id)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]provider.AgentEvent(nil), j.events...), nil
}

// Cancel kills the process group and waits until the leader is reaped.
func (d *Driver) Cancel(ctx context.Context, id provider.AgentJobID) error {
	j, err := d.lookup(id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if j.state.Terminal() {
		d.mu.Unlock()
		return nil
	}
	j.state, j.err = provider.AgentRunCancelling, ErrRunCancelled
	d.mu.Unlock()
	_ = procgroup.Signal(j.pgid, procgroup.SigKill)
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return provider.ErrCancelUnconfirmed
	}
}

// Collect waits for a terminal job and returns its filtered result. A
// failed job returns its typed error and withholds its output.
func (d *Driver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	j, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return provider.CollectResult{}, ErrRunCancelled
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res := j.result
	if j.err != nil {
		res.Output = ""
	}
	return res, j.err
}
