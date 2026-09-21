// Package cascadepa is the cascade-pa BUILTIN plugin (02-TARGET-STRUCTURE's
// plugin catalog, runtime: builtin): it registers with the host's
// compile-time registry via plugin.RegisterBuiltin from its own init(), and
// houses the real `cascade chat` implementation (pacmd.NewChatCommand) plus
// in-chat command dispatch (DispatchInChatText). `cascade chat` itself is
// mounted DIRECTLY on root by cmd/cascade/builtin_plugins.go, never through
// this manifest — see the FIX-cascade-chat-client-wiring journal.
//
// CONTRACT CORRECTION (FIX-manifest-collision-and-conductor-seam, not the
// original ticket's own scope): T/S-43.T3's manifest originally declared a
// `chat` provides.commands entry, on the theory this plugin would be
// mounted through internal/plugins.BuiltinRegistry. That is impossible:
// pkg/plugin/validate.go's reservedCommandNames blocklist (rule R5, from
// 07-CLI-COMMAND-TREE.md) reserves "chat" as a HOST-OWNED core noun,
// exactly like "config"/"daemon" — a plugin manifest declaring it is
// rejected outright by BuiltinRegistry.Load with
// ErrCodeCommandNameCollision, and because Load aggregates rejections
// across the WHOLE compile-time snapshot into one error, every other
// builtin plugin's caller that treats a non-nil Load error as fatal (e.g.
// plugins/pbd's TestBuiltinRegistration) broke too, not just cascade-pa.
// The FIX-cascade-chat-client-wiring journal already concluded chat must
// mount directly and did so in cmd/cascade/builtin_plugins.go, but never
// removed the now-illegal-and-redundant manifest entry that caused the
// rejection in the first place — that omission is this fix. The manifest
// below declares zero commands: cascade-pa still registers as a builtin
// (its init()-time composition-root wiring for soul/review/digest chat
// integrations depends on that registration existing), it simply provides
// no CLI command through the manifest-v2 surface.
//
// Purpose: cascade-pa's builtin registration point; chat's real command
//
//	logic and in-chat dispatch live in this package and its cmd
//	subpackage, reached by direct mount, not by manifest declaration.
//
// Inputs: none beyond what plugin.RegisterBuiltin requires.
// Outputs: one BuiltinRegistration with an empty Provides.Commands.
// Constraints: imports pkg/plugin and this plugin's own cmd/ package ONLY,
//
//	never internal/** (Art.10.2, plugins-providers-boundary) — the daemon
//	wiring (internal/client over the unix socket) crosses that boundary
//	through cmd.SetClient, a seam this package's cmd subpackage defines
//	and a composition root injects; see cmd/chat.go's doc comment for why
//	that injection call itself is a recorded, out-of-scope deviation.
//
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3; manifest
//
//	command entry REMOVED — FIX-manifest-collision-and-conductor-seam.
//
// U/S-46.T4 CONTRACT NOTE (recorded, not silently skipped): that ticket's
// full_desc asks to "declare chat.topics_list and chat.threads_list in
// cascade-pa's §D-21 COMMANDS declaration and register them in
// plugins/cascade-pa/plugin.go via the builtin plugin host services".
// This manifest adds no Provides.Commands entry for them, for the same
// reason the comment block above gives for "chat" itself: --topics,
// --threads, and --thread <slug> are FLAGS of the already-reserved "chat"
// verb (rule R5, reservedCommandNames — pkg/plugin/validate.go), not new
// CLI verbs a CommandSpec could name, and chat.* is the CORE RPC
// namespace 07-CLI-COMMAND-TREE.md pins to the `chat` command (rpc:
// chat.*), registered by internal/conversation/adapter.go — outside this
// package's importable boundary (Art.10.2) and outside U/S-46.T4's
// files_scope. The real implementation lives in cmd/topics_cli.go's own
// TopicsThreadsClient seam, mirroring cmd/chat.go's Client precedent; see
// that file's header for the full gap record.
package cascadepa

import (
	"sync"

	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-pa"

// commandChat names the chat behavior this package implements
// (pacmd.NewChatCommand, handlers.RunCommand's name check). It is no
// longer declared in the manifest's Provides.Commands (see the package
// doc comment) — "chat" is a reserved core noun mounted directly by
// cmd/cascade/builtin_plugins.go, never through the plugin registry.
const commandChat = "chat"

// init registers cascade-pa with the host's compile-time registry. A
// blank-import of this package is sufficient to trigger this and make the
// plugin known to plugin.Builtins() — matching plugins/examples/
// example-builtin's established pattern exactly.
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns cascade-pa's cascade.plugin/v2 manifest. It declares no
// commands: "chat" is a reserved core noun (pkg/plugin/validate.go rule
// R5) that would collide were it listed here, and is instead mounted
// directly on root — see the package doc comment.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade Personal Assistant",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		// The MCP surface is generated from this manifest, so a tool
		// missing from this list is absent from every harness no matter
		// what DispatchTool can service. Built FROM the tools package
		// rather than repeating its constants, so the list a harness sees
		// and the list Dispatch routes cannot drift.
		Provides: plugin.Provides{Tools: toolSpecs()},
	}
}

// SetConversations injects the conversation service the three tools call.
//
// A seam, like pacmd.SetClient beside it, and for the same reason: this
// package may not import internal/** (Art.10.2, R-14.69), and the only
// implementation lives at the composition root over the daemon's chat.*
// methods. Until it is called the tools refuse with tools.ErrNoService,
// which is a different answer from "you have no conversations".
func SetConversations(c tools.Conversations) {
	toolState.mu.Lock()
	toolState.dispatcher = tools.NewDispatcher(c)
	toolState.mu.Unlock()
}

// toolState holds the injected dispatcher. Guarded because SetConversations
// runs at daemon startup while DispatchTool can be called from any harness
// session concurrently.
var toolState struct {
	mu         sync.Mutex
	dispatcher *tools.Dispatcher
}

// activeDispatcher returns the injected dispatcher, or one over no service
// (which refuses) when nothing has been injected.
func activeDispatcher() *tools.Dispatcher {
	toolState.mu.Lock()
	defer toolState.mu.Unlock()
	if toolState.dispatcher == nil {
		return tools.NewDispatcher(nil)
	}
	return toolState.dispatcher
}

// toolSpecs renders the tools package's registration order as manifest
// entries.
func toolSpecs() []plugin.ToolSpec {
	names := tools.Names()
	out := make([]plugin.ToolSpec, 0, len(names))
	for _, n := range names {
		out = append(out, plugin.ToolSpec{Name: n, Description: tools.Description(n)})
	}
	return out
}
