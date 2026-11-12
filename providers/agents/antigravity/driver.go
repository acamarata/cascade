package antigravity

// Purpose: expose the pinned agent provider and preserve job identity.
// Inputs: host-validated job specifications and lifecycle requests.
// Outputs: process-group jobs and filtered results, or typed refusals.
// Constraints: exact host environment, closed argv and no inherited custody.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07e).

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

// Driver implements the external print-job provider.
type Driver struct {
	cfg     Config
	probe   ProbeResult
	refusal error
	approve chan provider.ApprovalRequest
	mu      sync.Mutex
	jobs    map[provider.AgentJobID]*job
}

type job struct {
	cmd    *exec.Cmd
	pgid   int
	done   chan struct{}
	state  provider.AgentRunState
	result provider.CollectResult
	err    error
}

var _ provider.AgentProvider = (*Driver)(nil)
var _ provider.ExecutableReporter = (*Driver)(nil)

// New probes the binary without inheriting the operator's environment.
// Absent or changed interfaces yield an inert driver; version drift stays distinct.
func New(cfg Config) (*Driver, error) {
	if cfg.Binary == "" {
		cfg.Binary = "agy"
	}
	d := &Driver{cfg: cfg, jobs: make(map[provider.AgentJobID]*job), approve: make(chan provider.ApprovalRequest)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var help []byte
	d.probe = Probe(ctx, func(string) (string, error) { return exec.LookPath(cfg.Binary) },
		func(ctx context.Context, bin string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, bin, "--help")
			cmd.Env = []string{"HOME=" + cfg.RuntimeHome}
			var err error
			help, err = cmd.Output()
			return help, err
		})
	if d.probe.Outcome != ProbePresent {
		d.refusal = ErrInterfaceAbsent
	} else {
		d.refusal = CheckHarness(d.probe.Version, help)
	}
	if err := checkDefaults(cfg.Defaults); err != nil {
		return nil, err
	}
	return d, nil
}

// Executable reports the configured binary, including on a refused driver.
func (d *Driver) Executable() string { return d.cfg.Binary }

// Capabilities returns the configured compliance posture.
func (d *Driver) Capabilities(context.Context, string) (provider.Capabilities, error) {
	if d.refusal != nil {
		return provider.Capabilities{}, d.refusal
	}
	return provider.Capabilities{CompliancePosture: d.cfg.Posture}, nil
}

// SupportedProtocols reports the pinned range, or an empty range on refusal.
func (d *Driver) SupportedProtocols() provider.ProtocolRange {
	if d.refusal != nil {
		return provider.ProtocolRange{}
	}
	return ourProtocolRange
}

// Negotiate checks construction refusal before negotiating the protocol pin.
func (d *Driver) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	if d.refusal != nil {
		return "", d.refusal
	}
	return Negotiate(peer)
}

func (d *Driver) modelError() error {
	if d.refusal != nil {
		return d.refusal
	}
	return errUnsupportedModelOp
}

// Chat refuses model calls on the agent surface.
func (d *Driver) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, d.modelError()
}

// Embed refuses model calls on the agent surface.
func (d *Driver) Embed(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, d.modelError()
}

// Count refuses model calls on the agent surface.
func (d *Driver) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, d.modelError()
}

// Stream refuses model calls on the agent surface.
func (d *Driver) Stream(context.Context, provider.ChatRequest, provider.StreamSink) error {
	return d.modelError()
}

// ApprovalRequests returns the driver's approval channel.
func (d *Driver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.approve }

// ResolveApproval refuses requests because print mode has no approval transport.
func (d *Driver) ResolveApproval(string, string) error { return d.modelError() }

// Message refuses mid-run turns for a known job.
func (d *Driver) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	if _, err := d.lookup(id); err != nil {
		return err
	}
	return provider.ErrMessageUnsupported
}

func (d *Driver) lookup(id provider.AgentJobID) (*job, error) {
	if d.refusal != nil {
		return nil, d.refusal
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.jobs[id]
	if j == nil {
		return nil, provider.ErrJobNotFound
	}
	return j, nil
}

// Spawn validates custody before starting one print process in its own group.
func (d *Driver) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	if err := d.preflight(spec); err != nil {
		return provider.SpawnResult{}, err
	}
	argv, err := BuildArgv(d.cfg.Binary, d.cfg.Defaults, spec, spec.Envelope.ExpiresAt.Sub(d.cfg.Clock.Now()))
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
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.SysProcAttr = append([]string(nil), spec.Env.Driver...), spec.WorktreeRoot, procgroup.Attr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return provider.SpawnResult{}, ErrStart
	}
	if err = cmd.Start(); err != nil {
		return provider.SpawnResult{}, ErrStart
	}
	pgid, err := procgroup.Lookup(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return provider.SpawnResult{}, ErrStart
	}
	j := &job{cmd: cmd, pgid: pgid, done: make(chan struct{}), state: provider.AgentRunRunning,
		result: provider.CollectResult{JobID: id, DataClass: spec.DataClass, Artifacts: []string{}}}
	d.jobs[id] = j
	go d.readJob(j, bufio.NewScanner(stdout), spec.Filter)
	return provider.SpawnResult{JobID: id, ProcessGroupID: pgid}, nil
}

func (d *Driver) readJob(j *job, scanner *bufio.Scanner, filter provider.OutputFilter) {
	decoder, _ := NewDecoder(filter)
	var failure error
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		if _, failure = decoder.Decode(scanner.Bytes()); failure != nil {
			_ = procgroup.Signal(j.pgid, procgroup.SigKill)
			break
		}
	}
	if scanner.Err() != nil && failure == nil {
		failure = ErrMalformedEvent
	}
	if err := j.cmd.Wait(); err != nil && failure == nil {
		failure = ErrNonZeroExit
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if j.err == nil {
		j.err = failure
	}
	j.result.Output = decoder.Output()
	switch j.err {
	case nil:
		j.state = provider.AgentRunAccepted
	case ErrRunCancelled:
		j.state = provider.AgentRunCancelled
	default:
		j.state = provider.AgentRunFailed
	}
	close(j.done)
}

// Status reports the current job state.
func (d *Driver) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	j, err := d.lookup(id)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return j.state, nil
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

// Collect waits for a terminal job and returns its filtered result.
func (d *Driver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	j, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	select {
	case <-j.done:
		d.mu.Lock()
		defer d.mu.Unlock()
		return j.result, j.err
	case <-ctx.Done():
		return provider.CollectResult{}, ErrRunCancelled
	}
}

// Artifacts returns a fresh slice of the job's discovered paths.
func (d *Driver) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	if _, err := d.lookup(id); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// newJobID returns a fresh job id when the spec names none.
func newJobID() provider.AgentJobID {
	var b [16]byte
	_, _ = io.ReadFull(cryptorand.Reader, b[:])
	return provider.AgentJobID("antigravity-" + hex.EncodeToString(b[:]))
}
