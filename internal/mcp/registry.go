// Package mcp implements the MCP tool registry and the transport-agnostic
// dispatch core (see server.go). This file: sources tool definitions from
// compile-time-registered plugin manifests (pkg/plugin.Builtins, C-S05.T6) and
//
//	applies a fail-closed policy filter before any tool is exposed to an
//	MCP client. This is a security boundary, not a feature: a plugin this
//	filter cannot positively prove safe is excluded, never included by
//	default.
//
// Inputs: a ManifestSource (pkg/plugin.Builtins in production; tests inject
//
//	synthetic sources to exercise the filter without depending on which
//	real plugins happen to be compiled in).
//
// Outputs: Tool (the MCP-facing tool descriptor) and the filtered List().
// Constraints: no bare time.Now/rand; fail-closed policy (see
//
//	isExposable's doc comment) — an empty, nil, or unrecognized Grants
//	entry excludes the whole plugin's tools, never defaults to exposing
//	them.
//
// SPORT: internal/mcp [ADD] (P1-E04-W1-S06-T6 sport_updates).
package mcp

import (
	"context"
	"fmt"
	"sort"

	"github.com/acamarata/cascade/pkg/plugin"
)

// Tool is one MCP-exposed tool descriptor (tools/list's wire shape).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PluginID    string `json:"plugin_id"`
	// InputSchema is the tool's JSON Schema for its arguments, emitted
	// verbatim in tools/list (P1-E16-W4-S34-T2, R-14.251 rule 1).
	//
	// A nil schema is not a defect and is not invented around: the plugin
	// manifest format carries no per-tool schema, so a manifest-sourced
	// tool has none to declare. initialize.go emits the permissive object
	// schema the protocol requires in that case, and says why. A tool that
	// DOES declare one is describing arguments its handler really reads.
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// ManifestSource returns the current set of compile-time-registered plugin
// registrations. plugin.Builtins is the production implementation; it is
// injected here (rather than called directly) so registry_test.go can
// exercise the policy filter against synthetic registrations, independent
// of whatever plugins happen to be blank-imported into a given test
// binary.
type ManifestSource func() []plugin.BuiltinRegistration

// CoreRegistration is a first-party tool registration. First-party tools
// traverse the same closed grant filter as plugin tools; callers cannot use
// this seam to publish a mutation tool with an unrecognized grant.
type CoreRegistration struct {
	Tool    Tool
	Grants  []string
	Handler func(context.Context, []byte) ([]byte, error)
	// RequiredCapability names the policy capability a caller must hold
	// for this tool to APPEAR in tools/list (P1-E16-W4-S34-T2). An empty
	// value means the tool declares no capability of its own and is
	// governed by Grants alone, which is every registration that predates
	// this field.
	//
	// A non-empty value is checked against the registry's CapabilityFilter
	// at construction. A registry built without one denies every such
	// tool: naming a capability and finding nothing to ask is exactly the
	// case that must not default to exposure.
	RequiredCapability string
}

// knownSafeGrants is the closed set of capability grants this ticket
// recognizes as compatible with MCP exposure. It intentionally contains
// exactly one entry: "read" is the only grant W1's plugin host confers
// (pkg/plugin/register.go: RegisterBuiltin always seeds Grants to
// ["read"]) and the only grant this ticket has any basis to call safe. A
// future capability-policy engine (out of this ticket's scope) is expected
// to replace this table wholesale, not extend it ad hoc.
var knownSafeGrants = map[string]bool{"read": true}

// isExposable reports whether grants makes a plugin's declared tools
// eligible for MCP exposure: fail-closed on every axis. Empty or nil
// grants -> false (a plugin proven to want nothing is not, on that
// account, proven safe to expose — absence of a claim is not a positive
// safety claim). Any single grant string this table does not recognize ->
// false for the WHOLE plugin, not just that grant: an unrecognized grant
// is exactly the "unparseable/unrecognized entry" hard requirement #1
// names, and it must never default to exposing anything.
func isExposable(grants []string) bool {
	if len(grants) == 0 {
		return false
	}
	for _, g := range grants {
		if !knownSafeGrants[g] {
			return false
		}
	}
	return true
}

// ToolRegistry loads, filters, and dispatches MCP tools.
type ToolRegistry struct {
	source ManifestSource
	tools  map[string]resolvedTool
	// filtered names every tool a capability check excluded, in sorted
	// order. It is reported by FilteredOut for the debug log this
	// ticket's contract asks for, and it never reaches an MCP client:
	// see Call's doc comment for why a client must not be able to tell a
	// denied tool from an absent one.
	filtered []string
}

// resolvedTool pairs a Tool descriptor with the handler that services it,
// so Call can dispatch without re-walking every registration.
type resolvedTool struct {
	descriptor Tool
	handler    func(context.Context, []byte) ([]byte, error)
}

// NewToolRegistry builds a ToolRegistry over source, computing the
// filtered tool set once at construction — the registry is read-only for
// its lifetime, matching the stateless-core convention this ticket's
// contract sets for the rest of the package.
// filter is consulted ONCE, here: the registry is read-only for its
// lifetime, so a grant change takes effect on the next registration pass,
// which is a new registry (P1-E16-W4-S34-T2). A nil filter is the
// fail-closed default, which withholds every tool that names a
// capability.
func NewToolRegistry(source ManifestSource, filter CapabilityFilter, core ...CoreRegistration) *ToolRegistry {
	if filter == nil {
		filter = DenyAllFilter{}
	}
	ctx := context.Background()
	r := &ToolRegistry{source: source, tools: make(map[string]resolvedTool)}
	for _, reg := range source() {
		if !isExposable(reg.Grants) {
			continue
		}
		for _, t := range reg.Manifest.Provides.Tools {
			handlers := reg.Handlers
			r.tools[t.Name] = resolvedTool{
				descriptor: Tool{Name: t.Name, Description: t.Description, PluginID: reg.Manifest.ID},
				handler: func(ctx context.Context, input []byte) ([]byte, error) {
					return handlers.DispatchTool(ctx, t.Name, input)
				},
			}
		}
	}
	for _, reg := range core {
		if !isExposable(reg.Grants) || reg.Handler == nil || reg.Tool.Name == "" {
			continue
		}
		if reg.RequiredCapability != "" && !filter.Allow(ctx, reg.RequiredCapability) {
			r.filtered = append(r.filtered, reg.Tool.Name)
			continue
		}
		r.tools[reg.Tool.Name] = resolvedTool{descriptor: reg.Tool, handler: reg.Handler}
	}
	sort.Strings(r.filtered)
	return r
}

// FilteredOut names every tool the capability filter excluded, sorted.
//
// It is for the composition root's debug log and for tests. It is
// deliberately NOT reachable from any wire surface: List omits these
// tools and Call reports them as unknown, so nothing a client can ask
// reveals that a denied tool exists.
func (r *ToolRegistry) FilteredOut() []string {
	out := make([]string, len(r.filtered))
	copy(out, r.filtered)
	return out
}

// List returns the policy-filtered tool set, sorted by name for
// deterministic wire output.
func (r *ToolRegistry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, rt := range r.tools {
		out = append(out, rt.descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call dispatches name with input to its plugin handler. name not being in
// the filtered set is indistinguishable, from the caller's side, between
// "does not exist" and "exists but was filtered out" — deliberately: an
// MCP client must never learn that a tool exists but was denied, which
// would leak the existence of privileged capability to an untrusted model.
func (r *ToolRegistry) Call(ctx context.Context, name string, input []byte) ([]byte, error) {
	rt, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return rt.handler(ctx, input)
}
