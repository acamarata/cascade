package plugin

import (
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: typed struct tree for the cascade.plugin/v2 manifest schema —
//   the external contract every first-party and third-party plugin is
//   written against.
// Inputs: none (pure type declarations).
// Outputs: Manifest and its component types, plus the ValidationError /
//   ErrCode sub-taxonomy that Validate (validate.go) and ParseManifest
//   (loader.go) report through.
// Constraints: pkg/plugin never imports internal/ (Art.10.2); every
//   exported symbol carries Godoc (Art.10.6); no bare fmt.Errorf/errors.New
//   (boundary lint, internal/build/boundary_test.go).
// SPORT: pkg/plugin manifest-v2-schema (ADD) — P1-E03-W1-S05-T6.

// SchemaVersion is the only accepted value of a manifest's schema field.
// Any other value is a hard rejection (rule R1; see Validate).
const SchemaVersion = "cascade.plugin/v2"

// RuntimeMode identifies how the host executes a plugin: as code linked
// into the cascade binary (RuntimeBuiltin), as a supervised child process
// speaking the plugin RPC wire protocol (RuntimeProcess), as a wazero-hosted
// WebAssembly module (RuntimeWasm), or as a remote endpoint reached over the
// network (RuntimeRemote). 02-TARGET-STRUCTURE.md §Key contracts names all
// four modes; the First-party plugin catalog v1 table exercises three of
// them (builtin, process, wasm) in this ticket's golden fixtures.
type RuntimeMode string

// The closed set of RuntimeMode values. An unrecognized string in a
// manifest's runtime field is rejected by rule R2 (ErrCodeUnknownRuntimeMode).
const (
	// RuntimeBuiltin is code compiled into the cascade binary itself.
	RuntimeBuiltin RuntimeMode = "builtin"
	// RuntimeProcess is a supervised child process speaking plugin RPC.
	RuntimeProcess RuntimeMode = "process"
	// RuntimeWasm is a wazero-hosted WebAssembly module.
	RuntimeWasm RuntimeMode = "wasm"
	// RuntimeRemote is a remote endpoint reached over the network.
	RuntimeRemote RuntimeMode = "remote"
)

// Valid reports whether m is one of the four RuntimeMode values the host
// recognizes. The zero value (empty string) is not valid.
func (m RuntimeMode) Valid() bool {
	switch m {
	case RuntimeBuiltin, RuntimeProcess, RuntimeWasm, RuntimeRemote:
		return true
	default:
		return false
	}
}

// IntentSpec declares one natural-language capability a plugin can satisfy.
// The host's intent install flow (02-TARGET-STRUCTURE.md §Key contracts)
// resolves an unmet intent by searching the registry/catalog for a plugin
// whose provides.intents entry matches, then walks the user through
// confirm → verify → enable before the agent resumes.
type IntentSpec struct {
	// Name is the intent identifier the host matches against (required).
	Name string `toml:"name"`
	// Description is the human-readable summary shown during intent
	// resolution and in plugin listings.
	Description string `toml:"description"`
}

// ToolSpec declares one agent-callable tool the plugin exposes. Tools are
// the function-calling surface an agent session can invoke once the plugin
// is enabled and granted the capabilities it requires.
type ToolSpec struct {
	// Name is the tool identifier, namespaced by convention as
	// "<plugin-id>.<tool>" (required).
	Name string `toml:"name"`
	// Description is the human- and model-readable summary of what
	// invoking the tool does.
	Description string `toml:"description"`
}

// DomainSpec declares one storage domain the plugin registers. Per
// 02-TARGET-STRUCTURE.md §Key contracts ("Storage scoping"), a plugin gets
// a namespaced PluginStorage area per declared domain; reaching outside a
// declared domain requires an explicit capability grant.
type DomainSpec struct {
	// Name is the domain identifier, scoped under the plugin's own
	// namespace by the host (required).
	Name string `toml:"name"`
	// Description is the human-readable summary of what the domain
	// stores.
	Description string `toml:"description"`
}

// CommandSpec declares one CLI verb (and, optionally, the JSON-RPC method
// it mounts to) that the plugin contributes to the host's command tree, per
// 10-ROUND2-DELTAS.md §D-21 ("manifest v2 provides gains commands ...
// CLI+RPC mounting"). A CommandSpec whose Name collides with a core noun or
// reserved utility verb from 07-CLI-COMMAND-TREE.md is rejected by rule R5
// (ErrCodeCommandNameCollision).
type CommandSpec struct {
	// Name is the CLI verb mounted under the plugin's namespace
	// (required). Must not collide with a host-reserved core noun or
	// utility verb.
	Name string `toml:"name"`
	// Description is the one-line summary shown in `cascade help`.
	Description string `toml:"description"`
	// RPCMethod is the JSON-RPC method this command mounts to on the
	// daemon side. Empty means the command is CLI-only (no RPC mount).
	RPCMethod string `toml:"rpc_method"`
}

// Provides groups everything a plugin contributes to the host: agent
// intents it can satisfy, tools it exposes, storage domains it registers,
// and CLI/RPC commands it mounts (02-TARGET-STRUCTURE.md §Key contracts;
// 10-ROUND2-DELTAS.md §D-21).
type Provides struct {
	// Intents is the set of natural-language capabilities this plugin can
	// satisfy via the intent install flow.
	Intents []IntentSpec `toml:"intents,omitempty"`
	// Tools is the set of agent-callable tools this plugin exposes.
	Tools []ToolSpec `toml:"tools,omitempty"`
	// Domains is the set of storage domains this plugin registers.
	Domains []DomainSpec `toml:"domains,omitempty"`
	// Commands is the set of CLI verbs (with optional RPC mounts) this
	// plugin contributes to the host's command tree.
	Commands []CommandSpec `toml:"commands,omitempty"`
}

// PermissionDisplay is one entry in the user consent dialog shown before a
// plugin is enabled (02-TARGET-STRUCTURE.md §Key contracts: "permissions
// display"). Name identifies the underlying capability being requested;
// Description is the human-readable sentence the consent UI renders.
type PermissionDisplay struct {
	// Name is the capability identifier this permission entry displays
	// (required).
	Name string `toml:"name"`
	// Description is the human-readable consent-dialog sentence
	// (required).
	Description string `toml:"description"`
}

// Manifest is the fully parsed cascade.plugin/v2 manifest: the external
// contract between a plugin author and the host. Every field is normative
// per 02-TARGET-STRUCTURE.md §Key contracts ("Plugin manifest v2"). A
// Manifest is only ever produced by ParseManifest (loader.go), which
// fail-closes: a caller never receives a Manifest alongside a non-nil
// error.
type Manifest struct {
	// ID uniquely identifies the plugin across the host registry. Must
	// match [a-z][a-z0-9-]* (required).
	ID string `toml:"id"`
	// Name is the plugin's human display name (required).
	Name string `toml:"name"`
	// Schema must equal SchemaVersion exactly; any other value is a hard
	// rejection (rule R1).
	Schema string `toml:"schema"`
	// Version is the plugin's own semver version string (required).
	Version string `toml:"version"`
	// HostVersion is the semver range of host versions this plugin
	// expects to run under (required).
	HostVersion string `toml:"host_version"`
	// Runtime selects how the host executes this plugin (required).
	Runtime RuntimeMode `toml:"runtime"`
	// Provides declares everything this plugin contributes to the host.
	Provides Provides `toml:"provides"`
	// Requires lists the capability names (not driver names — 02
	// §Key contracts: "requires (capabilities not drivers)") this plugin
	// needs granted before the host will enable it.
	Requires []string `toml:"requires"`
	// Permissions lists the consent-dialog entries shown to the user
	// before enabling this plugin.
	Permissions []PermissionDisplay `toml:"permissions"`
}

// ErrCode identifies which of ParseManifest/Validate's named rejection
// rules produced a ValidationError. It is a plugin-package-local
// sub-taxonomy layered on top of pkg/cascade's Kind enumeration (R-14.2):
// every ErrCode maps to exactly one cascade.Kind via ErrCode.Kind, which is
// the Kind ParseManifest's returned *cascade.Error carries. ErrCode exists
// because a single cascade.Kind (e.g. KindInvalidInput) is too coarse for a
// plugin author to tell "empty id" apart from "malformed semver" — the
// ErrCode is the machine-readable discriminator; the cascade.Kind is the
// wire-stable severity/response class.
type ErrCode string

// The closed set of manifest ErrCode values, one per named rejection rule
// (see Validate in validate.go) plus ErrCodeParse for TOML decode failures,
// including strict-mode unknown-key rejection (loader.go).
const (
	// ErrCodeParse reports that the TOML document itself could not be
	// decoded into a Manifest, including a strict-mode unknown top-level
	// key.
	ErrCodeParse ErrCode = "parse"
	// ErrCodeSchemaVersion reports rule R1: schema != SchemaVersion.
	ErrCodeSchemaVersion ErrCode = "schema-version"
	// ErrCodeUnknownRuntimeMode reports rule R2: an unrecognized runtime
	// string.
	ErrCodeUnknownRuntimeMode ErrCode = "unknown-runtime-mode"
	// ErrCodeRequiredField reports rule R3: an empty id or name.
	ErrCodeRequiredField ErrCode = "required-field"
	// ErrCodeMalformedVersion reports rule R4: version or host_version is
	// not a well-formed semver value.
	ErrCodeMalformedVersion ErrCode = "malformed-version"
	// ErrCodeCommandNameCollision reports rule R5: a provides.commands
	// entry whose name collides with a reserved core noun or utility
	// verb.
	ErrCodeCommandNameCollision ErrCode = "command-name-collision"
	// ErrCodeInvalidCapabilityRef reports rule R6: an empty or duplicate
	// requires entry.
	ErrCodeInvalidCapabilityRef ErrCode = "invalid-capability-ref"
)

// ValidationError is one rejection finding from Validate: which Field it
// concerns, which named rule (Kind) fired, and a human-readable Message.
// ValidationError implements the error interface so a single finding can be
// wrapped directly into a *cascade.Error via cascade.Wrap (loader.go uses
// this for ParseManifest's fail-closed return).
type ValidationError struct {
	// Field is the manifest field path the finding concerns, e.g. "id",
	// "version", or "provides.commands[2].name".
	Field string
	// Kind is the named rejection rule (an ErrCode, not a cascade.Kind —
	// see ErrCode's doc comment) that produced this finding.
	Kind ErrCode
	// Message is the human-readable explanation.
	Message string
}

// Error implements the error interface, formatting as
// "<field>: <errcode>: <message>".
func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Field, e.Kind, e.Message)
}

// errCodeKind maps each ErrCode to the single cascade.Kind ParseManifest's
// returned *cascade.Error carries for a rejection of that kind. Every
// manifest rejection is, from the host's perspective, either a malformed
// document the caller must fix (KindInvalidInput), a schema version the
// host does not support (KindUnsupported), or a name that conflicts with a
// reserved host command (KindConflict) — see the DECISIONS note in this
// ticket's journal for why R1/R5 are singled out from the otherwise-uniform
// KindInvalidInput mapping.
var errCodeKind = map[ErrCode]cascade.Kind{
	ErrCodeParse:                cascade.KindInvalidInput,
	ErrCodeSchemaVersion:        cascade.KindUnsupported,
	ErrCodeUnknownRuntimeMode:   cascade.KindInvalidInput,
	ErrCodeRequiredField:        cascade.KindInvalidInput,
	ErrCodeMalformedVersion:     cascade.KindInvalidInput,
	ErrCodeCommandNameCollision: cascade.KindConflict,
	ErrCodeInvalidCapabilityRef: cascade.KindInvalidInput,
}

// Kind returns the cascade.Kind that ParseManifest's returned *cascade.Error
// carries for a ValidationError of this ErrCode. Unrecognized ErrCode
// values (which cannot occur for an ErrCode produced by this package) fall
// back to cascade.KindInternal.
func (c ErrCode) Kind() cascade.Kind {
	if k, ok := errCodeKind[c]; ok {
		return k
	}
	return cascade.KindInternal
}
