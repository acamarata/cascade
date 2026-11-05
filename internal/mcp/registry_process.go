// Package mcp (registry_process.go): Purpose: admit the MCP tools of
// installed process plugins into a ToolRegistry under a fail-closed
// exposure rule distinct from the builtin read-only rule in registry.go.
// Inputs: a ProcessToolSource (internal/plugins.InstalledToolSource in
// production) whose registrations carry the embedded manifest, the
// plugin's stored effective grants and the call that reaches it.
// Outputs: NewToolRegistryWithProcess and ToolRegistry.WithProcess.
// Constraints: a plugin is exposable only when its manifest's Requires is
// non-empty with no empty entry, the set of its Grants equals the set of
// its Requires, it has a Call, and every tool it declares carries the
// prefix Manifest.ID+"." and collides with no builtin, core or earlier
// process tool. One violation excludes the whole plugin. A nil source
// contributes nothing. registry.go's isExposable and the builtin path are
// untouched by this file.
// SPORT: internal/mcp installed process tools (P1-PLG-06).
package mcp

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/plugin"
)

// ProcessToolRegistration is one installed process plugin offered for MCP
// exposure: its embedded manifest, the grants its stored record confers,
// and the call that reaches the plugin for one of its tools.
type ProcessToolRegistration struct {
	Manifest plugin.Manifest
	Grants   []string
	Call     func(ctx context.Context, tool string, input []byte) ([]byte, error)
}

// ProcessToolSource returns the process plugins currently offered for
// exposure. It is called once per registry construction.
type ProcessToolSource func() []ProcessToolRegistration

// NewToolRegistryWithProcess builds the registry NewToolRegistry builds
// over source, filter and core, then adds every process plugin process
// offers that passes the exposure rule in this file's header. A process
// tool never shadows a builtin or core tool, whether or not that tool was
// itself exposed.
func NewToolRegistryWithProcess(source ManifestSource, process ProcessToolSource, filter CapabilityFilter, core ...CoreRegistration) *ToolRegistry {
	r := NewToolRegistry(source, filter, core...)
	reserved := reservedToolNames(r)
	for _, reg := range core {
		if reg.Tool.Name != "" {
			reserved[reg.Tool.Name] = true
		}
	}
	addProcessTools(r, process, reserved)
	return r
}

// WithProcess returns a new registry holding r's tools plus every process
// plugin process offers that passes the exposure rule. r is unchanged, so
// a composite built this way keeps every filter decision r already made.
func (r *ToolRegistry) WithProcess(process ProcessToolSource) *ToolRegistry {
	out := &ToolRegistry{source: r.source, tools: make(map[string]resolvedTool, len(r.tools)),
		filtered: append([]string(nil), r.filtered...)}
	for name, rt := range r.tools {
		out.tools[name] = rt
	}
	addProcessTools(out, process, reservedToolNames(r))
	return out
}

// ProcessExposable reports whether reg alone satisfies the exposure rule:
// non-empty Requires, set(Grants) == set(Requires), a Call, and every tool
// named under the plugin's prefix. Collisions need the registry and are
// checked when it is built; a plugin's own call path re-runs this rule
// before every call.
func ProcessExposable(reg ProcessToolRegistration) bool {
	return reg.Call != nil && processGrantsExposable(reg.Manifest.Requires, reg.Grants) &&
		processToolNamesValid(reg.Manifest, nil)
}

// reservedToolNames is every name a process tool may not take: r's tools,
// the tools r's filter withheld, and every tool a builtin manifest
// declares, exposed or not.
func reservedToolNames(r *ToolRegistry) map[string]bool {
	reserved := make(map[string]bool, len(r.tools)+len(r.filtered))
	for name := range r.tools {
		reserved[name] = true
	}
	for _, name := range r.filtered {
		reserved[name] = true
	}
	if r.source != nil {
		for _, reg := range r.source() {
			for _, t := range reg.Manifest.Provides.Tools {
				reserved[t.Name] = true
			}
		}
	}
	return reserved
}

// addProcessTools adds each admitted plugin's tools to r. Admitted names
// join reserved, so a later plugin cannot take an earlier one's tool.
func addProcessTools(r *ToolRegistry, process ProcessToolSource, reserved map[string]bool) {
	if process == nil {
		return
	}
	for _, reg := range process() {
		if reg.Call == nil || !processGrantsExposable(reg.Manifest.Requires, reg.Grants) ||
			!processToolNamesValid(reg.Manifest, reserved) {
			continue
		}
		call := reg.Call
		for _, t := range reg.Manifest.Provides.Tools {
			name := t.Name
			reserved[name] = true
			r.tools[name] = resolvedTool{
				descriptor: Tool{Name: name, Description: t.Description, PluginID: reg.Manifest.ID},
				handler: func(ctx context.Context, input []byte) ([]byte, error) {
					return call(ctx, name, input)
				},
			}
		}
	}
}

// processGrantsExposable reports whether grants and requires are the same
// non-empty set with no empty entry: a missing grant and an extra grant
// both refuse.
func processGrantsExposable(requires, grants []string) bool {
	want, ok := grantSet(requires)
	if !ok || len(want) == 0 {
		return false
	}
	have, ok := grantSet(grants)
	if !ok || len(have) != len(want) {
		return false
	}
	for g := range have {
		if !want[g] {
			return false
		}
	}
	return true
}

// grantSet returns entries as a set; an empty entry is refused.
func grantSet(entries []string) (map[string]bool, bool) {
	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			return nil, false
		}
		set[e] = true
	}
	return set, true
}

// processToolNamesValid reports whether every tool m declares is named
// m.ID+".<leaf>", appears once, and is not in reserved.
func processToolNamesValid(m plugin.Manifest, reserved map[string]bool) bool {
	if m.ID == "" {
		return false
	}
	prefix := m.ID + "."
	seen := make(map[string]bool, len(m.Provides.Tools))
	for _, t := range m.Provides.Tools {
		if !strings.HasPrefix(t.Name, prefix) || len(t.Name) == len(prefix) || seen[t.Name] || reserved[t.Name] {
			return false
		}
		seen[t.Name] = true
	}
	return true
}
