// Purpose: the harness binding table: which plugin owns each agent harness,
// which driver factory builds it, and the closed credential table that is
// the only source of a harness's environment variable names. Plus the
// read-only listing and the Resolver the host shim uses.
// Inputs: Binding rows and factories registered at init by harness_<id>.go;
// a PluginPresence callback; a lane string.
// Outputs: Binding copies, DriverFactory values and typed refusals.
// Constraints: RegisterHarness panics on a duplicate or malformed row, at
// init, so a bad table never ships; Bindings returns a sorted deep copy;
// Resolve never substitutes another harness: an unknown lane, or a lane
// whose owning plugin is absent or disabled, is KindUnavailable.
// SPORT: internal.providers.agents/ADD (P1-AGT-02, contract:harness-agent-binding).

package agents

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// HarnessID names one external agent harness.
type HarnessID string

// RuntimeKind is how a harness runs; harness-cli is the only P1 value.
type RuntimeKind string

// RuntimeHarnessCLI is a vendor CLI the driver spawns as a child process.
const RuntimeHarnessCLI RuntimeKind = "harness-cli"

// LanePrefix prefixes a harness id in a lane name ("agent:claude").
const LanePrefix = "agent:"

// The closed CredentialSpec.Class set.
const (
	CredentialSubscription = "subscription"
	CredentialOAuthToken   = "oauth_token"
	CredentialAPIKey       = "api_key"
	CredentialCompatKey    = "compat_key"
)

// CredentialSpec is one credential class a harness accepts and the single
// environment variable that carries it. KeyVar is "" for subscription.
type CredentialSpec struct {
	Class  string
	KeyVar string
}

// Binding binds a harness to its owning plugin and credential table. Anything
// added to CredentialSpec later (a map field) must also be deep-copied by copyBinding.
type Binding struct {
	Harness HarnessID
	// PluginID is the owning plugin's manifest id.
	PluginID string
	Runtime  RuntimeKind
	// LaneID is LanePrefix + Harness.
	LaneID string
	// ConfigDirVar is the vendor config-directory variable.
	ConfigDirVar string
	// Credentials is the closed per-harness credential table (B-1).
	Credentials []CredentialSpec
	// Binaries are the vendor executable basenames, for the exec gate.
	Binaries []string
}

// Clock is the injected time source a factory hands its driver.
type Clock interface{ Now() time.Time }

// SpawnSite is everything a factory needs to build one driver. Clock is the
// one field beyond the contract's list: drivers may not read the wall clock.
type SpawnSite struct {
	KeyVar          string
	Binary          string
	Posture         provider.CompliancePosture
	Defaults        provider.DriverDefaults
	RuntimeHome     string
	VendorAuthVar   string
	CredentialClass string
	WorktreeRoot    string
	HookPackPath    string
	Clock           Clock
}

// DriverFactory builds a harness driver for site; the result must also
// implement provider.ExecutableReporter.
type DriverFactory func(site SpawnSite) (provider.AgentProvider, error)

// PluginPresence reports whether a plugin is installed and enabled.
type PluginPresence func(pluginID string) (enabled bool)

type entry struct {
	binding Binding
	factory DriverFactory
}

// registry is a harness table; the package keeps one, tests build others.
type registry struct {
	mu   sync.RWMutex
	rows map[HarnessID]entry
}

var global = &registry{rows: map[HarnessID]entry{}}

// knownHarnesses is the closed set RegisterHarness accepts. The local model
// lane is not a harness (ADR-0002 section 1) and is deliberately absent.
var knownHarnesses = map[HarnessID]bool{"claude": true, "codex": true, "opencode": true, "antigravity": true}

// RegisterHarness adds a harness to the table and returns an empty value so
// a registration can be a package-level `var _ = RegisterHarness(...)`. It
// panics on a harness outside the closed set, a malformed row or a duplicate.
func RegisterHarness(b Binding, f DriverFactory) struct{} {
	if !knownHarnesses[b.Harness] {
		panic(fmt.Sprintf("agents: RegisterHarness(%q): not one of the closed harness set", b.Harness))
	}
	global.add(b, f)
	return struct{}{}
}

func (r *registry) add(b Binding, f DriverFactory) {
	if err := validateBinding(b); err != nil {
		panic(fmt.Sprintf("agents: RegisterHarness(%q): %v", b.Harness, err))
	}
	if f == nil {
		panic(fmt.Sprintf("agents: RegisterHarness(%q): nil driver factory", b.Harness))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.rows[b.Harness]; dup {
		panic(fmt.Sprintf("agents: harness %q registered twice", b.Harness))
	}
	r.rows[b.Harness] = entry{binding: copyBinding(b), factory: f}
}

// Bindings lists every registered binding sorted by harness id. The result
// is a copy: changing it changes nothing. It is the read-only listing and
// needs no import of providers/agents/*.
func Bindings() []Binding { return global.list() }

func (r *registry) list() []Binding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Binding, 0, len(r.rows))
	for _, e := range r.rows {
		out = append(out, copyBinding(e.binding))
	}
	slices.SortFunc(out, func(a, b Binding) int { return strings.Compare(string(a.Harness), string(b.Harness)) })
	return out
}

func copyBinding(b Binding) Binding {
	b.Credentials = slices.Clone(b.Credentials)
	b.Binaries = slices.Clone(b.Binaries)
	return b
}

var envVarName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// validateBinding checks one row against the closed credential table rules:
// a non-empty plugin, the harness-cli runtime, LaneID = LanePrefix+Harness, a
// config-directory variable, binaries that are bare basenames, and per
// credential a class from the closed set whose key variable is present for
// key classes and absent for subscription.
func validateBinding(b Binding) error {
	switch {
	case b.Harness == "" || strings.ContainsAny(string(b.Harness), ": /"):
		return fmt.Errorf("harness id %q is empty or not a bare name", b.Harness)
	case b.PluginID == "":
		return fmt.Errorf("owning plugin id is empty")
	case b.Runtime != RuntimeHarnessCLI:
		return fmt.Errorf("runtime %q is not %q", b.Runtime, RuntimeHarnessCLI)
	case b.LaneID != LanePrefix+string(b.Harness):
		return fmt.Errorf("lane %q is not %q", b.LaneID, LanePrefix+string(b.Harness))
	case !envVarName.MatchString(b.ConfigDirVar):
		return fmt.Errorf("ConfigDirVar %q is not an environment variable name", b.ConfigDirVar)
	case len(b.Binaries) == 0:
		return fmt.Errorf("no vendor binaries listed")
	}
	for _, bin := range b.Binaries {
		if bin == "" || strings.ContainsAny(bin, `/\`) {
			return fmt.Errorf("binary %q is not a bare basename", bin)
		}
	}
	return validateCredentials(b.Credentials)
}

func validateCredentials(creds []CredentialSpec) error {
	if len(creds) == 0 {
		return fmt.Errorf("credential table is empty")
	}
	seen := map[string]bool{}
	for _, c := range creds {
		switch c.Class {
		case CredentialSubscription:
			if c.KeyVar != "" {
				return fmt.Errorf("subscription credential names key variable %q", c.KeyVar)
			}
		case CredentialOAuthToken, CredentialAPIKey, CredentialCompatKey:
			if !envVarName.MatchString(c.KeyVar) {
				return fmt.Errorf("%s credential key variable %q is not an environment variable name", c.Class, c.KeyVar)
			}
		default:
			return fmt.Errorf("credential class %q is outside the closed set", c.Class)
		}
		if seen[c.Class] {
			return fmt.Errorf("credential class %q listed twice", c.Class)
		}
		seen[c.Class] = true
	}
	return nil
}

// Resolver turns a lane into its binding and factory, checking that the
// owning plugin is present. Construct with NewResolver.
type Resolver struct {
	reg     *registry
	present PluginPresence
}

// NewResolver returns a Resolver over the registered harnesses. A nil
// present is KindInvalidInput: the host must say which plugins are enabled.
func NewResolver(present PluginPresence) (*Resolver, error) {
	return newResolver(global, present)
}

func newResolver(reg *registry, present PluginPresence) (*Resolver, error) {
	if present == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "agents: resolver needs a plugin presence callback")
	}
	return &Resolver{reg: reg, present: present}, nil
}

// Resolve returns the binding and factory for lane "agent:<harness>". A lane
// that does not name a registered harness is KindUnavailable; so is a harness
// whose owning plugin is absent or disabled, naming the plugin. No other
// harness is ever returned in its place.
func (r *Resolver) Resolve(lane string) (Binding, DriverFactory, error) {
	id, ok := strings.CutPrefix(lane, LanePrefix)
	r.reg.mu.RLock()
	e, found := r.reg.rows[HarnessID(id)]
	r.reg.mu.RUnlock()
	if !ok || !found {
		return Binding{}, nil, cascade.Newf(cascade.KindUnavailable, "agents: lane %q is not a registered harness lane", lane)
	}
	if !r.present(e.binding.PluginID) {
		return Binding{}, nil, cascade.Newf(cascade.KindUnavailable,
			"agents: harness %q is unavailable: owning plugin %q is absent or disabled", id, e.binding.PluginID)
	}
	return copyBinding(e.binding), e.factory, nil
}
