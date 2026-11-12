package antigravity

// Purpose: expose the pinned agent provider and preserve job identity.
// Inputs: host-validated job specifications and lifecycle requests.
// Outputs: process-group jobs and filtered results, or typed refusals.
// Constraints: exact host environment, closed argv and no inherited custody.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07e).

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"io"
	"os/exec"
	"sync"
	"time"

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
