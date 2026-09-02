// Package examplebuiltin is the toy first-party builtin plugin that proves
// the compile-time registration pattern (P1-E03-W1-S05-T7, the Epic C
// acceptance ticket): it registers one real tool, one real intent, and one
// real command via plugin.RegisterBuiltin from its own init(), so that a
// blank-import of this package is sufficient to make it known to the host.
//
// Purpose: a REAL (Art.1 — no panic("not implemented"), no Noop*)
//
//	minimal builtin plugin: DispatchTool/DispatchIntent/RunCommand all
//	produce genuine, deterministic output.
//
// Inputs: none beyond the arguments the host passes at dispatch time.
// Outputs: greeting text/errors from the tool and intent handlers; a
//
//	printed greeting (and nil error) from the command handler.
//
// Constraints: imports pkg/plugin ONLY, never internal/** (Art.10.2,
//
//	enforced by internal/build/arch_test.go's plugins-providers-boundary
//	rule); "greet" is not a reserved core noun or utility verb (verified
//	against pkg/plugin/validate.go's reservedCommandNames blocklist).
//
// SPORT: plugins/examples/example-builtin (ADD) — P1-E03-W1-S05-T7.
package examplebuiltin

import (
	"context"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "example-builtin"

// The three provided-surface names this plugin registers: one tool, one
// intent, one command, all sharing the "greet" verb by design (§WHAT).
const (
	toolName    = "greet-tool"
	intentName  = "greet-intent"
	commandName = "greet"
)

// init registers this plugin with the host's compile-time registry. A
// blank-import of this package (as cmd/cascade's composition root, or a
// test, performs) is sufficient to trigger this and make the plugin known
// to plugin.Builtins().
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns this plugin's cascade.plugin/v2 manifest.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Example Builtin",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Tools: []plugin.ToolSpec{
				{Name: toolName, Description: "Greets the caller by name."},
			},
			Intents: []plugin.IntentSpec{
				{Name: intentName, Description: "Satisfies a natural-language greeting request."},
			},
			Commands: []plugin.CommandSpec{
				{Name: commandName, Description: "Print a greeting."},
			},
		},
	}
}

// handlers is the REAL plugin.BuiltinHandlers implementation for
// example-builtin. It carries no state; every method is a pure function of
// its arguments.
type handlers struct{}

// DispatchTool services the greet-tool ToolSpec: input is the (optional)
// name to greet, trimmed; an empty input greets "world".
func (handlers) DispatchTool(_ context.Context, name string, input []byte) ([]byte, error) {
	if name != toolName {
		return nil, fmt.Errorf("example-builtin: unknown tool %q", name)
	}
	return []byte(greet(string(input))), nil
}

// DispatchIntent services the greet-intent IntentSpec identically to
// DispatchTool — a real natural-language greeting request resolves to the
// same greeting text a direct tool call would produce.
func (handlers) DispatchIntent(_ context.Context, name string, input []byte) ([]byte, error) {
	if name != intentName {
		return nil, fmt.Errorf("example-builtin: unknown intent %q", name)
	}
	return []byte(greet(string(input))), nil
}

// RunCommand services the greet CommandSpec: args[0], if present, is the
// name to greet; otherwise "world". The greeting is printed to stdout, as
// a real CLI command's output would be.
func (handlers) RunCommand(_ context.Context, name string, args []string) error {
	if name != commandName {
		return fmt.Errorf("example-builtin: unknown command %q", name)
	}
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	fmt.Println(greet(target))
	return nil
}

// greet formats the greeting text shared by all three handler methods. An
// empty (or whitespace-only) target greets "world".
func greet(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "world"
	}
	return "Hello, " + target + "!"
}
