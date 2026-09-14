// Package claude is the cascade-claude builtin harness plugin: it registers
// with the host's compile-time plugin registry (pkg/plugin.RegisterBuiltin)
// and delivers the install side of Epic P's primary harness integration —
// instruction install, hook-pack install and MCP registration.
//
// Purpose: make an installed harness aware of Cascade — instructions on
//
//	disk, session hooks posting to the daemon, and the Cascade MCP server
//	registered for harness sessions to call.
//
// The contract's other two capabilities, session watch and the paired
// uninstall hook, are NOT here. Each is blocked on a real missing
// counterpart rather than unfinished, and shipping either would have added
// a surface no running program could reach: the watch needs a client of the
// fleet sessions SSE stream (only the server half exists), and the uninstall
// must write an audit entry whose kind the ratified, closed audit taxonomy
// (R-21.235) does not contain. This ticket's journal records both with the
// exact blocker.
//
// Inputs: none beyond the arguments the host passes at dispatch time
// (RunCommand), the process environment the path resolver reads, and the
// two injected seams (Generate, RenderHookPack) that
// internal/plugins/claude_wiring.go wires to their real implementations.
// Outputs: files under the harness config root; errors for any failed step.
// Constraints: imports pkg/plugin, pkg/cascade and stdlib ONLY, never
//
//	internal/** (Art.10.2, plugins-providers-boundary depguard rule). Every
//	collaborator that lives under internal/ therefore enters through an
//	injected seam, the same bridge plugins/opencode/install.go documents.
//	Per R-14.51 there are NO `cascade harness install|uninstall` commands:
//	install fires via `cascade init` step 6 and `cascade context harness
//	sync`. Nothing here is elevated.
//
// SPORT: plugins/claude entity (ADD) — P1-E16-W4-S34-T1.
package claude

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-claude"

// packName is the hook-pack name this plugin registers and installs under.
// It is the same string the host's hook registry keys the pack by, so a
// rename here and a rename there cannot drift apart silently.
const packName = pluginID

// The command names this plugin registers, mirrored in manifest.toml.
// There is deliberately no "harness-install"/"harness-uninstall" CLI verb
// above these (R-14.51); they are plugin-scoped command names the host
// dispatches, exactly as cascade-opencode's are.
const cmdInstall = "install"

// init registers cascade-claude with the host's compile-time registry. A
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
		Name:        "Cascade Claude Harness",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: []plugin.CommandSpec{
				{Name: cmdInstall, Description: "Install the harness instruction golden, hook pack and MCP server entry."},
			},
		},
		Permissions: []plugin.PermissionDisplay{
			{Name: "read-context", Description: "Read the merged instruction context to render the harness instruction golden."},
			{Name: "hook-emit", Description: "Emit session-lifecycle events from the installed hook pack to the daemon."},
			{Name: "mcp-register", Description: "Register the Cascade MCP server in the harness MCP configuration."},
		},
	}
}

// handlers is the real plugin.BuiltinHandlers implementation for
// cascade-claude. It carries no state: every capability is a function of
// the injected seams and the process environment.
type handlers struct{}

// DispatchTool: cascade-claude declares no tools (manifest Provides.Tools
// is empty), so any call is a genuine "unsupported" outcome, not a stub.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-claude: no such tool %q: this plugin declares no tools", name)
}

// DispatchIntent: cascade-claude declares no intents, for the same reason
// DispatchTool does not.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-claude: no such intent %q: this plugin declares no intents", name)
}

// RunCommand dispatches one of the manifest-declared commands.
func (handlers) RunCommand(ctx context.Context, name string, args []string) error {
	switch name {
	case cmdInstall:
		return runInstall(ctx, args)
	default:
		return fmt.Errorf("cascade-claude: unknown command %q", name)
	}
}
