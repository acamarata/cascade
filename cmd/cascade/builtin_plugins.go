// Purpose: mounts `cascade chat` directly under root and links
//
//	internal/plugins into the binary so its composition-root wiring
//	(cascadepa_wiring.go's real internal/client-backed adapter) actually
//	runs. Split out of root.go (Art.10.3's 300-line cap) the same way
//	root_paths.go's lazyPaths was split out.
//
// CONTRACT DISCOVERY (this fix, not the original ticket's own scope):
// P1-E20-W5-S43-T3 built `cascade chat` as a compile-time BUILTIN PLUGIN
// (plugins/cascade-pa's manifest, mountable only through
// internal/plugins.BuiltinRegistry). But pkg/plugin/validate.go's own
// reservedCommandNames blocklist (rule R5, transcribed from
// 07-CLI-COMMAND-TREE.md) lists "chat" as one of the 14 RESERVED CORE
// NOUNS a plugin manifest command name must never collide with --
// BuiltinRegistry.Load rejects cascade-pa's manifest outright
// (ErrCodeCommandNameCollision) precisely because "chat" is reserved for
// a HOST-OWNED command, exactly like "config"/"daemon"/"provider" (which
// mount directly in root.go, never through the plugin registry). The
// plugin-manifest design and the reserved-name rule directly contradict
// each other; the manifest was invalid from the moment both landed. This
// fix does not touch pkg/plugin/validate.go's fail-closed rule (Art.1's
// authorization/classification checks fail closed, and the reserved list
// exists precisely to keep "chat" host-owned) -- instead it mounts chat
// the way every other reserved core noun already is: directly, by
// importing plugins/cascade-pa/cmd's cobra command constructor, never
// through BuiltinRegistry.NewCobraCommand.
//
// Inputs: none beyond plugins/cascade-pa/cmd.NewChatCommand's own
//
//	construction (no external state at mount time).
//
// Outputs: `cascade chat` mounted on root with the real
//
//	internal/client-backed Client wired in (see the blank import below).
//
// Constraints: the blank import of internal/plugins is REQUIRED, not
//
//	decorative -- it is the only production call site that runs that
//	package's init() functions, including cascadepa_wiring.go's
//	pacmd.SetClient(realAdapter) call and registry.go's pre-existing
//	cascade-codex/cascade-opencode generator adapters, none of which ran
//	in a shipped binary before this fix (cmd/cascade imported
//	internal/plugins nowhere until now).
//
// SPORT: cmd/cascade:chat-mount (ADD) -- FIX-cascade-chat-client-wiring.
package main

import (
	"github.com/spf13/cobra"

	// Blank import: links internal/plugins' init()-time composition-root
	// wiring (cascadepa_wiring.go's pacmd.SetClient call) into this
	// binary. No exported symbol from this package is used directly --
	// `cascade chat` mounts through pacmd below, per the CONTRACT
	// DISCOVERY note above, not through BuiltinRegistry.
	_ "github.com/acamarata/cascade/internal/plugins"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// mountChatCmd attaches `cascade chat` directly under root, matching
// mountConfigCmd/mountDaemonCmd's own pattern for every other reserved
// core noun.
func mountChatCmd(root *cobra.Command) {
	cmd := pacmd.NewChatCommand()
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}
