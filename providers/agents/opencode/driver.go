// Package opencode implements the one-shot OpenCode JSON driver.
package opencode

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// VendorAuthVar names the profile's config directory.
const VendorAuthVar = "XDG_CONFIG_HOME"

// KeyVarCompatKey names the environment credential for the compat provider.
const KeyVarCompatKey = "ZHIPU_API_KEY"

// ProtocolRevision pins the native event mapping revision.
const ProtocolRevision provider.ProtocolVersion = "1"

// Clock supplies the runtime clock without reading wall time in the driver.
type Clock interface{ Now() time.Time }

// Config holds the host-owned spawn configuration.
type Config struct {
	Binary        string
	Posture       provider.CompliancePosture
	Defaults      provider.DriverDefaults
	RuntimeHome   string
	VendorAuthVar string
	Clock         Clock
	ContentMatch  func([]byte) (string, bool)
}

// Driver owns the ordered event streams and process groups of its jobs.
type Driver struct {
	cfg     Config
	mu      sync.Mutex
	jobs    map[provider.AgentJobID]*job
	approve chan provider.ApprovalRequest
}

// Driver errors retain distinct sentinel identities within the frozen taxonomy.
var (
	ErrEmptySpawnEnv     = cascade.New(cascade.KindInvalidInput, "opencode: spawn environment is empty")
	ErrEmptyWorktreeRoot = cascade.New(cascade.KindInvalidInput, "opencode: worktree root is empty")
	ErrEnvNotAllowed     = cascade.New(cascade.KindPolicyDenied, "opencode: environment variable outside allowlist")
	ErrHomeMismatch      = cascade.New(cascade.KindPermissionDenied, "opencode: runtime home mismatch")
	ErrCredentialMissing = cascade.New(cascade.KindPermissionDenied, "opencode: compat credential missing")
	ErrDriverNotFound    = cascade.New(cascade.KindNotFound, "opencode: executable not found")
	ErrStart             = cascade.New(cascade.KindUnavailable, "opencode: process failed to start")
	ErrMalformedEvent    = cascade.New(cascade.KindIntegrity, "opencode: malformed JSON event")
	ErrUnknownEventKind  = cascade.New(cascade.KindIntegrity, "opencode: unknown event type")
	ErrNonZeroExit       = cascade.New(cascade.KindUnavailable, "opencode: process exited non-zero")
	ErrMissingTerminal   = cascade.New(cascade.KindIntegrity, "opencode: stream ended without terminal event")
	ErrRunCancelled      = cascade.New(cascade.KindCanceled, "opencode: job cancelled")
	ErrNoClock           = cascade.New(cascade.KindInvalidInput, "opencode: runtime clock is required")
	ErrInvalidConfig     = cascade.New(cascade.KindInvalidInput, "opencode: unsupported configuration")
	ErrDuplicateJob      = cascade.New(cascade.KindConflict, "opencode: job id already exists")
	errUnsupported       = cascade.New(cascade.KindUnsupported, "opencode: operation unsupported")
)

// New constructs a driver; a nil Clock permits capability probes only.
func New(cfg Config) (*Driver, error) {
	if cfg.Binary == "" {
		return nil, ErrInvalidConfig
	}
	if cfg.VendorAuthVar == "" {
		cfg.VendorAuthVar = VendorAuthVar
	}
	if cfg.VendorAuthVar != VendorAuthVar || !validDefaults(cfg.Defaults) {
		return nil, ErrInvalidConfig
	}
	if cfg.Defaults.CancelGrace <= 0 {
		cfg.Defaults.CancelGrace = provider.NewDriverDefaults().CancelGrace
	}
	return &Driver{cfg: cfg, jobs: make(map[provider.AgentJobID]*job), approve: make(chan provider.ApprovalRequest)}, nil
}

func validDefaults(d provider.DriverDefaults) bool {
	return d.SessionNameFlag == "" && d.KernelPromptFile == "" && d.SubagentKernelPromptFile == "" && d.PluginDir == "" && d.FallbackModel == "" && d.MCPConfigPath == "" && d.MaxTurns == 0 && d.MaxBudgetUSD == 0 && validEffort(d.Effort)
}

func validEffort(s string) bool {
	return s == "" || s == "minimal" || s == "low" || s == "medium" || s == "high" || s == "max"
}

// Executable reports the exact vendor executable.
func (d *Driver) Executable() string { return d.cfg.Binary }

// Capabilities returns the configured compliance posture.
func (d *Driver) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{CompliancePosture: d.cfg.Posture}, nil
}

// SupportedProtocols returns the single mapping revision.
func (d *Driver) SupportedProtocols() provider.ProtocolRange {
	return provider.ProtocolRange{Min: ProtocolRevision, Max: ProtocolRevision}
}

// Negotiate refuses disjoint protocol ranges before a run.
func (d *Driver) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(d.SupportedProtocols(), peer)
}

// Chat refuses model calls on an agent driver.
func (d *Driver) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errUnsupported
}

// Embed refuses model calls on an agent driver.
func (d *Driver) Embed(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, errUnsupported
}

// Count refuses model calls on an agent driver.
func (d *Driver) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, errUnsupported
}

// Stream refuses model calls on an agent driver.
func (d *Driver) Stream(context.Context, provider.ChatRequest, provider.StreamSink) error {
	return errUnsupported
}

// ApprovalRequests is a usable channel; native run has no approval transport.
func (d *Driver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.approve }

// ResolveApproval refuses because native run has no approval transport.
func (d *Driver) ResolveApproval(string, string) error { return errUnsupported }

// Message refuses follow-up turns for known jobs by exact sentinel identity.
func (d *Driver) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	if _, err := d.lookup(id); err != nil {
		return err
	}
	return provider.ErrMessageUnsupported
}

func (d *Driver) lookup(id provider.AgentJobID) (*job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	j, ok := d.jobs[id]
	if !ok {
		return nil, provider.ErrJobNotFound
	}
	return j, nil
}

var _ provider.AgentProvider = (*Driver)(nil)
