// Package process implements the trusted-tier-gated execution host for
// external process-tier plugins: stdio JSON-RPC 2.0 transport, version
// handshake, crash-isolated restart, and the plugin-process egress class
// registration seam.
//
// Purpose: the wire and manifest types the process-plugin runtime speaks:
//
//	the JSON-RPC 2.0 framing envelopes, the handshake pair, the
//	process-tier manifest shape, and the typed errors this package
//	returns.
//
// Inputs: none (pure type declarations).
// Outputs: Request/Response/Notification, HelloMsg/HelloAckMsg, Manifest,
//
//	TrustTier, and the package's sentinel errors.
//
// Constraints: 06-FORGE-SPEC's manifest v2 schema (pkg/plugin.Manifest,
//
//	P1-E03-W1-S05-T6) carries no trust_tier or net-scopes field — only
//	Requires ([]string capability names) and Permissions
//	([]PermissionDisplay). Editing pkg/plugin/manifest.go is out of this
//	ticket's files_scope, so Manifest here is a process-runtime-local
//	type carrying exactly the fields Launch's contract needs. See the
//	ticket journal for the full contradiction, both sides quoted.
//	Every sentinel below wraps exactly one frozen pkg/cascade Kind
//	(R-14.2): domain-specific sentinels live in their owning package,
//	never in pkg/cascade itself. The egress types below (EgressClass,
//	SensitivityTier, EgressRegistrationConfig) are this package's OWN
//	local mirror of internal/hooks/egress's types, not that package's
//	types themselves: golangci's plugins-providers-boundary depguard rule
//	(12-QUALITY-CONSTITUTION Art.10.2) forbids any internal/plugins/**
//	non-test file from importing internal/** at all, egress included, so
//	this package cannot hold an internal/hooks/egress.EgressClass. A
//	composition-root package outside this ticket's files_scope adapts
//	EgressRegistrar/EgressInterceptor to the real egress.Registry/
//	egress.Engine, translating between the two string-based type sets
//	(same underlying values, distinct named types). See the ticket
//	journal for the contract-vs-tree contradiction this resolves.
//
// SPORT: internal/plugins/process types (ADD) — P1-E15-W4-S31-T3.
package process

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EgressClass is this package's own view of an egress-inventory class
// identifier. It intentionally does NOT alias internal/hooks/egress.
// EgressClass (see the package doc comment); a composition-root adapter
// converts between the two.
type EgressClass string

// EgressClassPluginProcess is the egress class covering a launched
// plugin's stdio streams (06-FORGE-SPEC §5.17, §5.21). The os/exec fork
// itself registers no class of its own (R-21.265); this class governs
// only the substitution pass over what crosses the stdio boundary.
const EgressClassPluginProcess EgressClass = "plugin-process"

// pluginProcessEgressOwner names the ticket that owns this class, per the
// H/S-16.T1 inventory's Owner convention.
const pluginProcessEgressOwner = "O/S-31.T3"

// SensitivityTier is this package's own view of the egress firewall's
// classification a caller declares for outbound content. The values
// match internal/hooks/egress.SensitivityTier's string constants
// exactly, so a composition-root adapter's conversion is a plain string
// cast, not a lookup table.
type SensitivityTier string

// The tiers InterceptClass callers may declare. TierInternal is what
// this package's own stdio substitution call uses (see runtime.go): the
// bytes cross a process boundary on the operator's own machine, which is
// internal, not public, and the class is registered without
// AllowRestricted so a restricted classification would simply refuse.
// internal/hooks/egress additionally defines TierRestricted and
// TierLocalOnly; this package omits both because nothing here ever
// declares stdio content at either tier — a composition-root adapter
// converts by matching string value, not by exhausting this set.
const (
	TierInternal SensitivityTier = "internal"
	TierPublic   SensitivityTier = "public"
)

// EgressRegistrationConfig is this package's own view of the policy a
// registrant declares for one egress class, mirroring
// internal/hooks/egress.InterceptConfig's fields this ticket needs.
type EgressRegistrationConfig struct {
	// Enabled reports whether the class may carry bytes at all.
	Enabled bool
	// AllowRestricted admits TierRestricted content on this class.
	AllowRestricted bool
	// AllowedTiers narrows admission to exactly these tiers.
	AllowedTiers []SensitivityTier
	// Owner names the ticket responsible for the class.
	Owner string
}

// EgressRegistrar is the seam onto the egress inventory a ProcessRuntime
// registers its class with. Register MUST be idempotent: a second call
// registering the same class must return nil, not an error — the
// concrete adapter (built by the composition root against the real
// egress.Registry) is responsible for translating that registry's
// ErrDuplicateClass into a nil return here.
type EgressRegistrar interface {
	Register(class EgressClass, cfg EgressRegistrationConfig) error
}

// EgressInterceptor is the seam onto the egress firewall a ProcessRuntime
// passes stdio substitution through.
type EgressInterceptor interface {
	InterceptClass(ctx context.Context, class EgressClass, tier SensitivityTier, content []byte) ([]byte, error)
}

// PluginProtocolVersion is the plugin RPC protocol version this host
// speaks and the minimum it accepts from a plugin, absent a manifest
// override. A plugin reporting a lower version fails the handshake.
const PluginProtocolVersion = "1.0.0"

// TrustTier classifies a process-plugin manifest's declared trust level.
// Only TrustTierTrusted may be launched; every other value, including the
// empty zero value, is refused.
type TrustTier string

// The two recognized tiers. Any other string, including the zero value,
// is untrusted for Launch's purposes.
const (
	// TrustTierUntrusted is the default, refused tier.
	TrustTierUntrusted TrustTier = "untrusted"
	// TrustTierTrusted is the one tier Launch will fork a process for.
	TrustTierTrusted TrustTier = "trusted"
)

// Manifest is the process-runtime-local view of a plugin manifest: the
// fields Launch needs to gate, warn about, and exec. See the package doc
// comment for why this is not pkg/plugin.Manifest.
type Manifest struct {
	// Name identifies the plugin in the consent warning and crash report.
	Name string
	// TrustTier gates Launch. Anything but TrustTierTrusted refuses.
	TrustTier TrustTier
	// NetScopes are the declared outbound scopes shown in the consent
	// warning before exec.
	NetScopes []string
	// Command is the executable Launch runs.
	Command string
	// Args are the command's arguments.
	Args []string
	// MinProtocolVersion, if set, overrides PluginProtocolVersion as the
	// minimum version this host accepts from the plugin.
	MinProtocolVersion string
}

// minProtocolVersion returns the manifest's override, or the package
// default when unset.
func (m Manifest) minProtocolVersion() string {
	if m.MinProtocolVersion != "" {
		return m.MinProtocolVersion
	}
	return PluginProtocolVersion
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Request is a JSON-RPC 2.0 request: a method call expecting a Response
// carrying the same ID.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response: exactly one of Result or Error is
// set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification: no ID, no Response
// expected.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// HelloMsg is the host's cascade.hello notification params: the minimum
// protocol version the host accepts.
type HelloMsg struct {
	MinProtocolVersion string `json:"min_protocol_version"`
}

// HelloAckMsg is the plugin's cascade.hello_ack response params: the
// plugin's own protocol version and its manifest hash.
type HelloAckMsg struct {
	ProtocolVersion string `json:"protocol_version"`
	ManifestHash    string `json:"manifest_hash"`
}

// VersionMismatchMsg is the cascade.version_mismatch notification params
// the host sends before terminating a plugin whose version is too old.
type VersionMismatchMsg struct {
	MinProtocolVersion string `json:"min_protocol_version"`
	PluginVersion      string `json:"plugin_version"`
}

// Sentinel errors. Each wraps exactly one frozen pkg/cascade Kind; callers
// match with errors.Is against the sentinel or cascade.HasKind against
// the Kind.
var (
	// ErrUntrustedPlugin reports a manifest whose TrustTier is not
	// TrustTierTrusted. No OS process is forked when this is returned.
	ErrUntrustedPlugin = errors.New("process: manifest is not trusted-tier")
	// ErrVersionMismatch reports a plugin whose reported protocol version
	// is below the host's minimum. The process is terminated cleanly
	// before this is returned.
	ErrVersionMismatch = errors.New("process: plugin protocol version below host minimum")
	// ErrPluginUnavailable reports a plugin that exhausted its restart
	// budget. Every call after this state transition returns it.
	ErrPluginUnavailable = errors.New("process: plugin unavailable after exhausting restart attempts")
	// ErrPlatformNotSupported reports a platform this runtime does not
	// launch processes on (Art.5).
	ErrPlatformNotSupported = errors.New("process: process plugins are not supported on this platform")
)

// wrapUntrusted builds the typed ErrUntrustedPlugin return.
func wrapUntrusted(name string, tier TrustTier) error {
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrUntrustedPlugin,
		"process: plugin %q has trust_tier %q, not %q; refusing to launch", name, string(tier), string(TrustTierTrusted))
}

// wrapVersionMismatch builds the typed ErrVersionMismatch return.
func wrapVersionMismatch(name, pluginVersion, minVersion string) error {
	return cascade.Wrapf(cascade.KindUnsupported, ErrVersionMismatch,
		"process: plugin %q reported protocol version %q, below host minimum %q", name, pluginVersion, minVersion)
}

// wrapUnavailable builds the typed ErrPluginUnavailable return.
func wrapUnavailable(name string, attempts int) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrPluginUnavailable,
		"process: plugin %q is unavailable after %d restart attempts", name, attempts)
}

// wrapPlatformNotSupported builds the typed ErrPlatformNotSupported
// return.
func wrapPlatformNotSupported(goos string) error {
	return cascade.Wrapf(cascade.KindUnsupported, ErrPlatformNotSupported,
		"process: process plugins require the daemon and are not launched on %s; run the daemon on linux or darwin", goos)
}
