// Purpose: mount the builtin plugins' own command namespaces on root, so a
//
//	first-party builtin plugin is reachable from the shipped binary.
//
// WHY THIS FILE EXISTS. The W3 hardening gate ran the TAGGED artifact and
//
//	found `cascade pbd` absent entirely: plugins/pbd registers itself from
//	its own init() (pkg/plugin.RegisterBuiltin), but nothing in cmd/ or
//	internal/ imported the package, so the init never ran, the manifest
//	never reached plugin.Builtins(), and the N-epic's whole CLI surface —
//	the O-epic host surface's dogfood proof — did not exist in the
//	product. No unit test could see it: every test that exercises pbd
//	imports the package itself. This is the same class
//	DEFECT-builtin-plugin-registry-unreachable.md records for codex and
//	opencode, caught this time in the artifact rather than in the tree.
//
// Inputs: the compile-time builtin registry (populated by the blank import
//
//	below) plus 07-CLI-COMMAND-TREE's list of plugin-contributed
//	namespaces.
//
// Outputs: `cascade <namespace> <command>` for every command the plugin's
//
//	manifest provides, dispatched through the registry's own
//	NewCobraCommand — the designed mount path, not a second one.
//
// Constraints: a namespace whose manifest FAILED to load still mounts, as a
//
//	command that refuses with the load error. A rejected manifest that
//	simply vanishes from the help output is the silent, total failure this
//	file was written to end (Art.1); the user must be told why.
//
// SPORT: cmd/cascade:plugin-namespaces (ADD) — P1-E14-W3-S30-T5 (Art.9).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"

	// Blank import: runs plugins/pbd's init(), which is the ONLY way its
	// manifest reaches pkg/plugin.Builtins(). Without this line the
	// namespace below mounts nothing.
	_ "github.com/acamarata/cascade/plugins/pbd"
)

// pluginNamespaces is the set of plugin-contributed namespaces this binary
// mounts, by manifest id (07-CLI-COMMAND-TREE §"Plugin-contributed
// namespaces"). It is an explicit list rather than "everything loaded":
// the harness-generator builtins (codex, opencode, claude) and cascade-pa
// are wired for their seams, not for a user-facing noun of their own, and
// mounting them would invent CLI surface the command tree never ratified.
var pluginNamespaces = []string{"pbd"}

// mountPluginNamespaceCmds attaches each namespace in pluginNamespaces,
// then the PROCESS-runtime plugins' own commands.
//
// The second call is here, not in root.go's mountSubcommands, because
// root.go is at Art.10.3's 300-line cap: this function is already the
// "mount the plugin-contributed surface" step of the composition, and the
// process tier is the other half of it (plugin_process_mount.go's header
// records why that half did not exist).
func mountPluginNamespaceCmds(root *cobra.Command) {
	registry := &plugins.BuiltinRegistry{}
	loadErr := registry.Load()
	for _, id := range pluginNamespaces {
		root.AddCommand(pluginNamespaceCmd(registry, id, loadErr))
	}
	mountProcessPluginCmds(root)
}

// pluginNamespaceCmd builds one namespace's parent command.
//
// When the plugin did not load, the parent still mounts and every
// invocation reports loadErr. The alternative — omitting it — is how this
// defect stayed invisible for a whole wave.
func pluginNamespaceCmd(registry *plugins.BuiltinRegistry, id string, loadErr error) *cobra.Command {
	reg, ok := registry.Get(id)
	if !ok {
		return unloadablePluginCmd(id, loadErr)
	}
	cmd := &cobra.Command{
		Use:   id,
		Short: reg.Manifest.Name,
	}
	for _, spec := range reg.Manifest.Provides.Commands {
		if sub, built := registry.NewCobraCommand(id, spec.Name); built {
			sub.Args = cobra.ArbitraryArgs
			cmd.AddCommand(sub)
		}
	}
	guardUnknownSubcommands(cmd)
	return cmd
}

// unloadablePluginCmd is the namespace a rejected or unregistered plugin
// mounts: present in the help output, refusing with the reason.
func unloadablePluginCmd(id string, loadErr error) *cobra.Command {
	return &cobra.Command{
		Use:   id,
		Short: "unavailable: the " + id + " plugin did not load",
		RunE: func(*cobra.Command, []string) error {
			if loadErr != nil {
				return cascade.Wrapf(cascade.KindUnavailable, loadErr,
					"cascade %s: the %s plugin is not available in this build", id, id)
			}
			return cascade.Newf(cascade.KindUnavailable,
				"cascade %s: the %s plugin is not registered in this build", id, id)
		},
	}
}
