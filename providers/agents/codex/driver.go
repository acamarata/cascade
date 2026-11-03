package codex

// Purpose: the codex App Server driver's configuration, typed errors and
// the provider methods that need no child.
// Inputs: Config from the harness binding (internal/providers/agents).
// Outputs: *Driver, a provider.AgentProvider and provider.ExecutableReporter.
// Constraints: stdio only (no net, net/http or crypto/tls); no internal
// imports; every error is a cascade sentinel of the frozen kind set.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// VendorAuthVar is the variable naming the codex config directory (the
// profile auth dir that holds the linked auth.json).
const VendorAuthVar = "CODEX_HOME"

// KeyVarAPIKey is the API-key credential variable of the codex harness.
const KeyVarAPIKey = "OPENAI_API_KEY"

// SubcommandAppServer is the one launch path: argv is [Binary, "app-server"].
const SubcommandAppServer = "app-server"

// ProtocolRevision is the committed schema revision (testdata/protocol/1).
const ProtocolRevision provider.ProtocolVersion = "1"

// PinnedCodexVersion is the codex version the revision-1 schema was taken
// from; a child reporting another version is refused.
const PinnedCodexVersion = "0.144.2"

// ClientName is the clientInfo.name sent in initialize.
const ClientName = "cascade"

// ClientVersion is the clientInfo.version sent in initialize.
const ClientVersion = "0.1.0"

var ourProtocolRange = provider.ProtocolRange{Min: ProtocolRevision, Max: ProtocolRevision}

// Typed errors. Callers compare by identity, never by errors.Is alone.
var (
	ErrForbiddenLaunchPath = cascade.New(cascade.KindPolicyDenied, "codex: launch path other than app-server is forbidden")
	ErrAutoApprove         = cascade.New(cascade.KindPolicyDenied, "codex: automatic approval or a full-access sandbox is forbidden")
	ErrEmptySpawnEnv       = cascade.New(cascade.KindInvalidInput, "codex: spawn environment is empty")
	ErrEmptyWorktreeRoot   = cascade.New(cascade.KindInvalidInput, "codex: worktree root is empty")
	ErrEnvNotAllowed       = cascade.New(cascade.KindPolicyDenied, "codex: spawn environment carries a variable outside the allowlist")
	ErrHomeMismatch        = cascade.New(cascade.KindPermissionDenied, "codex: HOME is not the spawn env home")
	ErrCodexHomeMismatch   = cascade.New(cascade.KindPermissionDenied, "codex: app server codexHome is not the profile auth dir")
	ErrStart               = cascade.New(cascade.KindUnavailable, "codex: app server failed to start")
	ErrMalformedFrame      = cascade.New(cascade.KindIntegrity, "codex: malformed app server frame")
	ErrForeignFrame        = cascade.New(cascade.KindIntegrity, "codex: response to a request this driver never sent")
	ErrServerError         = cascade.New(cascade.KindUnavailable, "codex: app server returned an error")
	ErrNonZeroExit         = cascade.New(cascade.KindUnavailable, "codex: app server exited non-zero")
	ErrApprovalTimeout     = cascade.New(cascade.KindTimeout, "codex: approval timed out; declined and cancelled")
	ErrApprovalDenied      = cascade.New(cascade.KindPolicyDenied, "codex: approval denied; declined and cancelled")
	ErrRunCancelled        = cascade.New(cascade.KindCanceled, "codex: job cancelled")
	ErrTurnFailed          = cascade.New(cascade.KindUnavailable, "codex: turn failed")
	ErrInvalidEffort       = cascade.New(cascade.KindInvalidInput, "codex: effort is not a codex reasoning effort")
	ErrNoClock             = cascade.New(cascade.KindInvalidInput, "codex: Config.Clock is nil, so the envelope expiry cannot be checked")
	ErrDuplicateJob        = cascade.New(cascade.KindConflict, "codex: job id already live")
	errUnsupportedModelOp  = cascade.New(cascade.KindUnsupported, "codex: the agent driver has no model-call surface")
)

// efforts is the closed ReasoningEffort set of the committed schema.
var efforts = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

// Config configures one driver. RuntimeHome is the per-profile runtime
// home; VendorAuthVar names the config-dir variable (CODEX_HOME). The
// Defaults flag fields must be empty: codex takes none of them.
type Config struct {
	Binary        string
	Posture       provider.CompliancePosture
	Defaults      provider.DriverDefaults
	RuntimeHome   string
	VendorAuthVar string
	HookPackPath  string
	// ContentMatch, when set, runs provider.PreSpawnScan over the worktree
	// before any child (the host shim scans too; this is defence in depth).
	ContentMatch func([]byte) (string, bool)
	// Clock supplies "now" for the envelope expiry check; nil is allowed
	// for a probe-only driver (Spawn refuses).
	Clock Clock
}

// Clock is the injected time source (the runtime.Clock method set).
type Clock interface{ Now() time.Time }

// Driver runs codex App Server jobs.
type Driver struct {
	cfg     Config
	approve chan provider.ApprovalRequest

	mu   sync.Mutex
	jobs map[provider.AgentJobID]*job
	pend map[string]*pendingApproval
}

// New validates cfg and builds a driver. Any flag value that would widen
// the sandbox or approve on its own refuses with ErrAutoApprove.
func New(cfg Config) (*Driver, error) {
	if err := ValidateLaunch([]string{cfg.Binary, SubcommandAppServer}); err != nil {
		return nil, err
	}
	if cfg.VendorAuthVar == "" {
		cfg.VendorAuthVar = VendorAuthVar
	}
	if cfg.VendorAuthVar != VendorAuthVar {
		return nil, cascade.Newf(cascade.KindInvalidInput, "codex: vendor auth variable %q is not %s", cfg.VendorAuthVar, VendorAuthVar)
	}
	if err := checkDefaults(cfg.Defaults); err != nil {
		return nil, err
	}
	base := provider.NewDriverDefaults()
	if cfg.Defaults.ApprovalTimeout <= 0 {
		cfg.Defaults.ApprovalTimeout = base.ApprovalTimeout
	}
	if cfg.Defaults.CancelGrace <= 0 {
		cfg.Defaults.CancelGrace = base.CancelGrace
	}
	return &Driver{
		cfg:     cfg,
		approve: make(chan provider.ApprovalRequest, approvalBuffer),
		jobs:    make(map[provider.AgentJobID]*job),
		pend:    make(map[string]*pendingApproval),
	}, nil
}

// ValidateLaunch accepts exactly [binary, "app-server"]; the deprecated
// mcp-server path, any other subcommand or extra argument refuses.
func ValidateLaunch(argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return cascade.New(cascade.KindInvalidInput, "codex: binary is empty")
	}
	if strings.Contains(filepath.Base(argv[0]), "mcp-server") {
		return ErrForbiddenLaunchPath
	}
	for _, a := range argv[1:] {
		if strings.Contains(a, "mcp-server") {
			return ErrForbiddenLaunchPath
		}
	}
	if len(argv) != 2 || argv[1] != SubcommandAppServer {
		return ErrForbiddenLaunchPath
	}
	return nil
}

// forbiddenTokens may appear in no flag or parameter value.
var forbiddenTokens = []string{
	"danger-full-access", "never", "acceptForSession", "acceptWithExecpolicyAmendment",
	"auto_review", "full-auto", "dangerously-bypass", "yolo", "approval_policy",
	"approvalPolicy", "sandbox_mode", "sandbox", "applyNetworkPolicyAmendment",
}

// checkDefaults refuses any flag-shaped default: codex reads none, and a
// value there is the only way a config override could reach the child.
func checkDefaults(d provider.DriverDefaults) error {
	vals := []string{d.SessionNameFlag, d.KernelPromptFile, d.SubagentKernelPromptFile, d.PluginDir, d.FallbackModel, d.MCPConfigPath, d.Effort}
	for _, v := range vals {
		for _, tok := range forbiddenTokens {
			if strings.Contains(v, tok) {
				return ErrAutoApprove
			}
		}
	}
	for _, v := range vals[:6] {
		if v != "" {
			return cascade.Newf(cascade.KindInvalidInput, "codex: driver default %q has no codex flag", v)
		}
	}
	if d.Effort != "" && !efforts[d.Effort] {
		return ErrInvalidEffort
	}
	return nil
}

// Executable reports the binary the driver launches.
func (d *Driver) Executable() string { return d.cfg.Binary }

// Capabilities returns the configured compliance posture.
func (d *Driver) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{CompliancePosture: d.cfg.Posture}, nil
}

// SupportedProtocols is revision 1 only.
func (d *Driver) SupportedProtocols() provider.ProtocolRange { return ourProtocolRange }

// Negotiate pins revision 1 or returns ErrHarnessIncompatible.
func (d *Driver) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(ourProtocolRange, peer)
}

// Chat refuses: the driver runs agent jobs, not model calls.
func (d *Driver) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errUnsupportedModelOp
}

// Embed refuses: see Chat.
func (d *Driver) Embed(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, errUnsupportedModelOp
}

// Count refuses: see Chat.
func (d *Driver) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, errUnsupportedModelOp
}

// Stream refuses: see Chat.
func (d *Driver) Stream(context.Context, provider.ChatRequest, provider.StreamSink) error {
	return errUnsupportedModelOp
}

// ApprovalRequests is the channel every approval request is surfaced on.
func (d *Driver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.approve }

// Message refuses mid-run turns for a known job (ErrMessageUnsupported).
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
