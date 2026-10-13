// Purpose: R-21.158 protocol pins (ProtocolRange {1,1}, Negotiate), the
//   R-16.47 platform refusal, R-14.230 session naming, and the
//   R-14.288/R-14.291 headless argv: the per-flag emission rule, version
//   gates, the ToolPolicy mapping and the runtime-home deny rules.
// Inputs: an argvInput assembled by Spawn after every guard passed.
// Outputs: the argv the job child execs; the probe's load-affecting flags.
// Constraints: every DriverDefaults-sourced flag is omitted when empty or
//   zero; --permission-mode bypassPermissions only for ToolPolicy all;
//   --bare and --max-budget-usd only for api_key lanes; --json-schema is
//   never emitted; --effort ultracode is refused; --setting-sources project
//   loads only the tree's .claude/settings.json (the hook pack's), never
//   user or local settings. The prompt is the last
//   argv token after a literal "--" (verified on 2.1.273: tokens after
//   "--" are positional) and a prompt starting with "-" is refused.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"context"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// protocolRevision1 is the only protocol revision this driver speaks; it
// is defined by the fixture set in testdata/protocol/1/.
const protocolRevision1 provider.ProtocolVersion = "1"

var ourProtocolRange = provider.ProtocolRange{Min: protocolRevision1, Max: protocolRevision1}

// SupportedProtocols returns {1,1}.
func (d *Driver) SupportedProtocols() provider.ProtocolRange { return ourProtocolRange }

// Negotiate pins one version from the intersection with peer, or returns
// ErrHarnessIncompatible at runtime (R-21.158).
func (d *Driver) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(ourProtocolRange, peer)
}

// platformRefusal returns ErrPlatform on windows, always: the windows twin
// has no process-group kill, no group-empty test and no O_NOFOLLOW, so it
// cannot meet the cancel and custody rules until a job-object twin exists
// (R-16.47 allowed a daemon-backed spawn; that waits for the twin).
func platformRefusal() error {
	if runtime.GOOS == "windows" {
		return provider.ErrPlatform
	}
	return nil
}

// sessionName is the R-14.230 session name: the ticket id when present,
// else the job id. It travels as an argument, never an env variable.
func sessionName(job provider.AgentJobSpec, id provider.AgentJobID) string {
	if job.TicketID != "" {
		return job.TicketID
	}
	return string(id)
}

// argvInput is everything buildArgv reads.
type argvInput struct {
	def     provider.DriverDefaults
	desc    Descriptor
	version string
	policy  string
	cred    CredentialClass
	name    string
	jobID   provider.AgentJobID
	root    string
	addDirs []string
	home    string
	prompt  string
	// model and effort are the per-job values (spec.Model, spec.Effort);
	// empty emits no flag, a non-empty effort overrides def.Effort.
	model  string
	effort string
}

// pluginDirEmitted reports whether --plugin-dir is set and the harness has
// it; below its gate the hook-pack copy is the declared fallback.
func (in argvInput) pluginDirEmitted() bool {
	ok, err := in.desc.supports("--plugin-dir", in.version)
	return in.def.PluginDir != "" && ok && err == nil
}

// validEfforts is the closed --effort set from the captured --help.
var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// validModel is the accepted --model spelling: an alias or a full name (for
// example "opus", "claude-opus-4-1" or "opus[1m]"), never a flag-shaped or
// spaced token.
var validModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)

// checkModelEffort refuses (KindInvalidInput) a model that is not a plain
// name or alias and an effort (the job's, else the driver default) outside
// the captured closed set; "" passes.
func checkModelEffort(model, effort, defaultEffort string) error {
	if effort == "" {
		effort = defaultEffort
	}
	if model != "" && !validModel.MatchString(model) {
		return cascade.Newf(cascade.KindInvalidInput, "claude: --model %q is not a model name", model)
	}
	if effort != "" && !validEfforts[effort] {
		return cascade.Newf(cascade.KindInvalidInput, "claude: --effort %q is not emitted", effort)
	}
	return nil
}

// leaseWithinRoots refuses a lease whose real path (symlinks resolved) is not
// at or under the real path of an envelope root: Check is lexical only, so a
// leased root that is a link out of the envelope would otherwise pass.
func leaseWithinRoots(leaseRoot string, roots []string) error {
	resolved, err := filepath.EvalSymlinks(leaseRoot)
	for _, r := range roots {
		root, rerr := filepath.EvalSymlinks(filepath.FromSlash(r))
		if rel, relErr := filepath.Rel(root, resolved); err == nil && rerr == nil && relErr == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return cascade.Wrap(cascade.KindPolicyDenied, provider.ErrEnvelopeIncomplete, "claude: the leased root resolves outside every envelope root")
}

// buildArgv returns the job's exact spawn argv.
func buildArgv(in argvInput) ([]string, error) {
	if in.prompt == "" || strings.HasPrefix(in.prompt, "-") {
		return nil, cascade.New(cascade.KindInvalidInput, "claude: prompt must be non-empty and must not start with \"-\"")
	}
	mapping, ok := in.desc.ToolPolicyMappings[in.policy]
	if !ok {
		return nil, provider.ErrInvalidToolPolicy
	}
	if err := in.desc.gate("--permission-prompts", in.version); err != nil {
		return nil, err
	}
	// --session-id derives from the job id and so repeats across daemon
	// restarts (ids restart per Driver); that is safe only because
	// --no-session-persistence is always emitted beside it.
	argv := []string{"-p", "--output-format", "json", "--no-session-persistence",
		"--session-id", provider.SessionIDFor(in.jobID), "--strict-mcp-config", "--setting-sources", "project"}
	for _, dir := range in.addDirs {
		argv = append(argv, "--add-dir", dir)
	}
	argv = append(argv, "--permission-prompts", "none")
	argv = append(argv, mapping...)
	argv = append(argv, "--disallowedTools")
	argv = append(argv, homeDenyRules(in.home)...)
	optional, err := defaultsFlags(in)
	if err != nil {
		return nil, err
	}
	argv = append(argv, optional...)
	argv = append(argv, laneFlags(in)...)
	return append(argv, "--", in.prompt), nil
}

// homeDenyRules are harness permission rules denying its file tools the
// runtime home, where the credential material and the canary live.
// An absolute path in a rule is written with a leading "//".
func homeDenyRules(home string) []string {
	pattern := "//" + strings.TrimPrefix(home, "/") + "/**"
	return []string{"Read(" + pattern + ")", "Edit(" + pattern + ")", "Write(" + pattern + ")"}
}

// defaultsFlags refuses a value for an unsupported flag, then emits each
// DriverDefaults flag only when set, version gated through the descriptor.
func defaultsFlags(in argvInput) ([]string, error) {
	if err := refuseUnsupportedDefaults(in.def); err != nil {
		return nil, err
	}
	var out []string
	d := in.def
	if d.KernelPromptFile != "" {
		out = append(out, "--append-system-prompt-file", d.KernelPromptFile)
	}
	if in.pluginDirEmitted() {
		out = append(out, "--plugin-dir", d.PluginDir)
	}
	effort := d.Effort
	if in.effort != "" {
		effort = in.effort
	}
	if err := checkModelEffort(in.model, in.effort, d.Effort); err != nil {
		return nil, err
	}
	if in.model != "" {
		out = append(out, "--model", in.model)
	}
	if effort != "" {
		out = append(out, "--effort", effort)
	}
	if d.FallbackModel != "" {
		out = append(out, "--fallback-model", d.FallbackModel)
	}
	if d.MCPConfigPath != "" {
		out = append(out, "--mcp-config", d.MCPConfigPath)
	}
	if d.SessionNameFlag != "" {
		out = append(out, d.SessionNameFlag, in.name)
	}
	return out, nil
}

// laneFlags emits --bare and --max-budget-usd for api_key lanes only.
func laneFlags(in argvInput) []string {
	if in.cred != CredentialClassAPIKey {
		return nil
	}
	out := []string{"--bare"}
	if in.def.MaxBudgetUSD > 0 {
		out = append(out, "--max-budget-usd", strconv.FormatFloat(in.def.MaxBudgetUSD, 'f', -1, 64))
	}
	return out
}

// probeFlags are the flags that change what the harness loads at init
// (tool roots, MCP config, plugins, permission mode), mirrored onto the
// probe so its init event describes the job's own configuration.
func probeFlags(in argvInput) []string {
	var out []string
	for _, dir := range in.addDirs {
		out = append(out, "--add-dir", dir)
	}
	if in.def.MCPConfigPath != "" {
		out = append(out, "--mcp-config", in.def.MCPConfigPath)
	}
	if in.pluginDirEmitted() {
		out = append(out, "--plugin-dir", in.def.PluginDir)
	}
	if in.policy == provider.ToolPolicyAll {
		out = append(out, in.desc.ToolPolicyMappings[provider.ToolPolicyAll]...)
	}
	return out
}

// newSessionToken mints the per-spawn 128-bit callback token from
// crypto/rand; a failed draw refuses the spawn rather than use a weak token.
func newSessionToken() (provider.SessionToken, error) {
	var tok provider.SessionToken
	if _, err := cryptoRead(tok[:]); err != nil {
		return tok, cascade.Wrapf(cascade.KindInternal, err, "claude: minting the session token")
	}
	return tok, nil
}
