// Package pbd is the first-party builtin cascade-pbd plugin: it registers
// the ratified `pbd validate` command (07-CLI-COMMAND-TREE.md's plugin-
// contributed pbd namespace) with the host's compile-time builtin registry
// (pkg/plugin.RegisterBuiltin), running the native tree-store/validator
// engine in internal/pews over the canonical PEWS ticket tree.
//
// Purpose: mount `validate` — and ONLY `validate` — through the builtin-
//
//	plugin boundary supplied by C/S-05.T7; T3 owns authoring, T4 owns
//	lint, both as later commands on this same manifest.
//
// Inputs: RunCommand's args: args[0] is the tree root, args[1] (optional)
//
//	overrides DefaultPhase.
//
// Outputs: nil on a clean tree; a *cascade.Error of kind KindInvalidInput
//
//	(fail closed) summarizing every violation otherwise — see validate.go.
//
// Constraints: imports pkg/** and this plugin's own internal/pews ONLY,
//
//	never the repo's internal/** (Art.10.2, internal/build/arch_test.go's
//	plugins-providers-boundary rule) — this is also why RunCommand cannot
//	format output through internal/output.Writer and instead folds every
//	violation into the returned error's message.
//
// SPORT: plugins/pbd (ADD) — P1-E14-W3-S28-T2.
package pbd

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "pbd"

// validateCommandName is the one CommandSpec this ticket mounts.
const validateCommandName = "validate"

// init registers cascade-pbd with the host's compile-time registry. A
// blank-import of this package by the binary's composition root (or a
// test) is sufficient to make it known to plugin.Builtins().
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns cascade-pbd's cascade.plugin/v2 manifest.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "PBD Engine",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: []plugin.CommandSpec{
				{
					Name:        validateCommandName,
					Description: "Validate the PEWS ticket tree for structural correctness.",
				},
			},
		},
	}
}

// handlers is cascade-pbd's plugin.BuiltinHandlers implementation.
type handlers struct{}

// DispatchTool always refuses: this ticket provides no tools.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "pbd: no tool named %q", name)
}

// DispatchIntent always refuses: this ticket provides no intents.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "pbd: no intent named %q", name)
}

// RunCommand services the validate CommandSpec: args[0] is the PEWS tree
// root (required); args[1], if present, overrides DefaultPhase. It
// returns nil on a clean tree and a fail-closed *cascade.Error describing
// every violation found otherwise.
func (handlers) RunCommand(_ context.Context, name string, args []string) error {
	if name != validateCommandName {
		return cascade.Newf(cascade.KindUnsupported, "pbd: no command named %q", name)
	}
	if len(args) == 0 || args[0] == "" {
		return cascade.New(cascade.KindInvalidInput, "pbd validate: a tree root argument is required")
	}
	phase := DefaultPhase
	if len(args) > 1 && args[1] != "" {
		phase = args[1]
	}
	return runValidateAndSummarize(args[0], phase)
}
