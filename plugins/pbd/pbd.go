// Package pbd is the first-party builtin cascade-pbd plugin: it registers
// the ratified `pbd validate` and `pbd lint` commands (07-CLI-COMMAND-TREE.md's
// plugin-contributed pbd namespace) with the host's compile-time builtin
// registry (pkg/plugin.RegisterBuiltin), running the native tree-store/
// validator/lint engine in internal/pews over the canonical PEWS ticket
// tree.
//
// Purpose: mount `validate` (T2) and `lint` (T4) — and ONLY those two —
//
//	through the builtin-plugin boundary supplied by C/S-05.T7; T3 owns
//	authoring as a later command on this same manifest.
//
// Inputs: RunCommand's args: args[0] is the tree root, args[1] (optional)
//
//	overrides DefaultPhase.
//
// Outputs: nil on a clean tree/lint; a *cascade.Error of kind
//
//	KindInvalidInput (fail closed) summarizing every violation/issue
//	otherwise — see validate.go and lint.go.
//
// Constraints: imports pkg/** and this plugin's own internal/pews ONLY,
//
//	never the repo's internal/** (Art.10.2, internal/build/arch_test.go's
//	plugins-providers-boundary rule) — this is also why RunCommand cannot
//	format output through internal/output.Writer and instead folds every
//	violation/issue into the returned error's message.
//
// SPORT: plugins/pbd (ADD) — P1-E14-W3-S28-T2; lint (ADD) — P1-E14-W3-S28-T4.
package pbd

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "pbd"

// validateCommandName and lintCommandName are the CommandSpecs T2 and T4
// mount respectively. Authoring (create/edit/move) is T3's; this file
// only adds lint alongside the already-landed validate.
const validateCommandName = "validate"
const lintCommandName = "lint"

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
				{
					Name:        lintCommandName,
					Description: "Lint every PEWS ticket contract for completeness against the Forge spec.",
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

// RunCommand services the validate and lint CommandSpecs: args[0] is the
// PEWS tree root (required); args[1], if present, overrides DefaultPhase.
// It returns nil on a clean tree/lint and a fail-closed *cascade.Error
// describing every violation/issue found otherwise.
func (handlers) RunCommand(_ context.Context, name string, args []string) error {
	if name != validateCommandName && name != lintCommandName {
		return cascade.Newf(cascade.KindUnsupported, "pbd: no command named %q", name)
	}
	if len(args) == 0 || args[0] == "" {
		return cascade.Newf(cascade.KindInvalidInput, "pbd %s: a tree root argument is required", name)
	}
	phase := DefaultPhase
	if len(args) > 1 && args[1] != "" {
		phase = args[1]
	}
	if name == lintCommandName {
		return runLintAndSummarize(args[0], phase)
	}
	return runValidateAndSummarize(args[0], phase)
}
