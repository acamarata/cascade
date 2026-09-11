// Package opencode is the cascade-opencode builtin harness plugin: it registers
// with the host's compile-time plugin registry (pkg/plugin.RegisterBuiltin)
// and exposes three real commands — detect, install, uninstall — over the
// Opencode CLI's AGENTS.md instruction golden.
//
// Purpose: detect the opencode binary in PATH, install the Opencode instruction
//
//	golden the Context Engine (internal/context) renders, and remove
//	exactly what install placed, idempotently in both directions.
//
// Inputs: none beyond the arguments the host passes at dispatch time
// (RunCommand) and the process's own PATH/working directory.
// Outputs: command output on stdout (matching the example-builtin
// precedent, plugins/examples/example-builtin/plugin.go); errors for any
// failed step.
// Constraints: imports pkg/plugin, pkg/cascade and stdlib ONLY, never
//
//	internal/** (Art.10.2, plugins-providers-boundary depguard rule) — the
//	real instruction generator (internal/context) is wired into this
//	package's Generate variable by internal/plugins/registry.go, the one
//	place in the tree allowed to import both sides. No MCP registration,
//	no hook packs, no session watch (cascade-claude only, P1-E16-W4-S34-T1).
//
// SPORT: plugins/opencode entity (ADD) — P1-E16-W4-S34-T3.
package opencode

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-opencode"

// The three command names this plugin registers, mirrored in manifest.toml.
const (
	cmdDetect    = "detect"
	cmdInstall   = "install"
	cmdUninstall = "harness-uninstall"
)

// init registers cascade-opencode with the host's compile-time registry. A
// blank-import of this package (internal/plugins/registry.go performs one
// in production; tests perform their own) is sufficient to make the plugin
// known to plugin.Builtins().
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns this plugin's cascade.plugin/v2 manifest, kept
// byte-for-byte consistent with manifest.toml.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade Opencode Harness",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: []plugin.CommandSpec{
				{Name: cmdDetect, Description: "Report whether the opencode binary is present in PATH."},
				{Name: cmdInstall, Description: "Install the Opencode AGENTS.md instruction golden for the current working directory."},
				{Name: cmdUninstall, Description: "Remove only the AGENTS.md files this plugin's install step placed."},
			},
		},
		Permissions: []plugin.PermissionDisplay{
			{Name: "read-context", Description: "Read the merged instruction context to render the Opencode instruction golden."},
		},
	}
}

// handlers is the real plugin.BuiltinHandlers implementation for
// cascade-opencode. It carries no state: Install/Uninstall/Detect are pure
// functions of the injected Generate variable and the process environment.
type handlers struct{}

// DispatchTool: cascade-opencode declares no tools (manifest Provides.Tools is
// empty), so any call is a genuine, real "unsupported" outcome, not a stub.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-opencode: no such tool %q: this plugin declares no tools", name)
}

// DispatchIntent: cascade-opencode declares no intents, for the same reason
// DispatchTool does not.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-opencode: no such intent %q: this plugin declares no intents", name)
}

// RunCommand dispatches one of the three manifest-declared commands.
func (handlers) RunCommand(ctx context.Context, name string, args []string) error {
	switch name {
	case cmdDetect:
		return runDetect()
	case cmdInstall:
		return runInstall(ctx, args)
	case cmdUninstall:
		return runUninstall(ctx, args)
	default:
		return fmt.Errorf("cascade-opencode: unknown command %q", name)
	}
}
