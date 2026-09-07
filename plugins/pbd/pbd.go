// Package pbd is the first-party builtin cascade-pbd plugin: it registers
// the ratified `pbd validate`, `pbd lint`, `pbd create`, `pbd edit`, and
// `pbd move` commands (07-CLI-COMMAND-TREE.md's plugin-contributed pbd
// namespace) with the host's compile-time builtin registry
// (pkg/plugin.RegisterBuiltin), running the native tree-store/validator/
// lint/authoring engine in internal/pews over the canonical PEWS ticket
// tree.
//
// Purpose: mount validate (T2), lint (T4), and create/edit/move (T3) —
//
//	and ONLY those five — through the builtin-plugin boundary supplied by
//	C/S-05.T7. Later PBD tickets own status/board/lifecycle/dispatch as
//	further commands on this same manifest.
//
// Inputs: RunCommand's args, decoded per-verb — see pbd.go's RunCommand,
//
//	validate.go, lint.go, and author.go for each verb's own arg shape.
//
// Outputs: nil on a clean tree/lint/authoring write; a *cascade.Error of
//
//	kind KindInvalidInput (fail closed) summarizing every violation/issue
//	otherwise — see validate.go, lint.go, and author.go.
//
// Constraints: imports pkg/** and this plugin's own internal/pews ONLY,
//
//	never the repo's internal/** (Art.10.2, internal/build/arch_test.go's
//	plugins-providers-boundary rule) — this is also why RunCommand cannot
//	format output through internal/output.Writer and instead folds every
//	violation/issue into the returned error's message.
//
// SPORT: plugins/pbd (ADD) — P1-E14-W3-S28-T2; lint (ADD) — P1-E14-W3-S28-T4;
// authoring (ADD) — P1-E14-W3-S28-T3.
package pbd

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "pbd"

// validateCommandName and lintCommandName are the CommandSpecs T2 and T4
// mount respectively. createCommandName, editCommandName, and
// moveCommandName (author.go) are T3's.
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
				{
					Name:        createCommandName,
					Description: "Author a new PEWS ticket at the canonical tree position its id implies.",
				},
				{
					Name:        editCommandName,
					Description: "Overwrite an existing PEWS ticket's contract in place.",
				},
				{
					Name:        moveCommandName,
					Description: "Relocate a PEWS ticket to a new canonical tree position.",
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

// RunCommand services the five mounted CommandSpecs. validate and lint
// take args[0] (tree root, required) and an optional args[1] phase
// override; create, edit, and move have their own arg shapes documented
// on runCreateCommand/runEditCommand/runMoveCommand (author.go). It
// returns nil on success and a fail-closed *cascade.Error otherwise.
func (handlers) RunCommand(_ context.Context, name string, args []string) error {
	switch name {
	case validateCommandName, lintCommandName:
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
	case createCommandName:
		return runCreateCommand(args)
	case editCommandName:
		return runEditCommand(args)
	case moveCommandName:
		return runMoveCommand(args)
	default:
		return cascade.Newf(cascade.KindUnsupported, "pbd: no command named %q", name)
	}
}
