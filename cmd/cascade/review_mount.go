// Purpose (this file): closes the two composition-root gaps the P1-E25-
//
//	W5-S52-T5 adversarial review found (REWORK verdict, 2026-09-21) --
//	BLOCK 1 (mount) and MAJOR 2 (RPC). Neither can be closed from
//	plugins/review/** itself: that package's files_scope cannot reach
//	cmd/cascade, and it may import pkg/** only, never internal/rpc
//	(Art.10.2). T0 decisions D1/D3 pre-authorize exactly this one new
//	file plus the two minimal call sites it needs (root.go's
//	mountSubcommands, daemon_unix_run.go's buildRPCServer) -- matching
//	cmd/cascade/builtin_plugins.go's mountChatCmd precedent for the SAME
//	"reserved-noun, direct-mount, not through the generic plugin
//	namespace registry" shape.
//
// Inputs: mountReviewCmd takes the *cobra.Command root every other mount*
//
//	function in this package takes. the daemon composition root registers reviewRPCHandler on the
//	*rpc.Registry the daemon composition root builds once at startup.
//
// Outputs: `cascade review` reachable as a top-level noun on the shipped
//
//	binary's cobra root (BLOCK 1, verified against the BUILT binary, not
//	only the in-process test driver); the daemon JSON-RPC method
//	"plugin.review.review" (MAJOR 2), dispatching to the exact same
//	plugins/review.reviewProvider seam the CLI reads, via the exported
//	review.Review() wrapper -- never a second construction path.
//
// Constraints: mountReviewCmd has no platform-specific code and is called
//
//	unconditionally from mountSubcommands, so it builds and mounts on
//	darwin/linux/windows alike (the ticket's own PLATFORM requirement).
//	the registration's only call site (daemon_unix_run.go) carries a
//	`//go:build !windows` tag already -- windows daemon support defers
//	entirely to internal/daemon's own build (daemon_windows.go), the
//	same asymmetry every other RPC registration in this tree already
//	has; this file adds no NEW one. Neither function touches
//	cmd/cascade/plugin.go, plugin_rpc.go, plugin_process_mount.go or
//	chat_wiring.go -- all under concurrent edit by other lanes this
//	phase (git status, verified before writing this file).
//
// SPORT: cmd/cascade:review-mount (ADD) -- FIX P1-E25-W5-S52-T5 (D1, D3).
package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// mountReviewCmd attaches `cascade review` directly under root (D1),
// matching mountChatCmd's identical direct-mount shape for a reserved
// core-tree noun: 07-CLI-COMMAND-TREE §review and R-16.58's literal
// "mounts `cascade review`" both name a TOP-LEVEL noun, which
// mountPluginNamespaceCmds' generic per-manifest-id nesting would instead
// spell `cascade cascade-review review` -- wrong. NewReviewCommand
// (plugins/review/cmd.go) is the exact command RunCommand's builtin
// dispatch also executes (plugin.go), so this mount adds no second,
// divergent construction.
func mountReviewCmd(root *cobra.Command) {
	cmd := reviewplugin.NewReviewCommand()
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// reviewRPCMethod is the JSON-RPC method daemon_unix_run.go binds. Literal
// per 07-CLI-COMMAND-TREE §review's "rpc: plugin.review.* pattern" and T0
// decision D3 (2026-09-21) -- NOT
// internal/plugins.BuiltinRegistry.RPCMethodName's generic
// "plugin.<pluginID>.<commandName>" shape, which would derive
// "plugin.cascade-review.review" instead; plugins/review/plugin.go's own
// manifest() CommandSpec.RPCMethod carries this same literal.
const reviewRPCMethod = "plugin.review.review"

// reviewRPCParams is the wire shape plugin.review.review takes: the same
// two fields provider.ReviewRequest carries that a caller supplies
// (Context is CLI/RPC-caller-supplied free text, not needed by either
// surface today, so it is omitted rather than wired unused).
type reviewRPCParams struct {
	Level string `json:"level"`
	Diff  string `json:"diff"`
}

// reviewRPCHandler implements rpc.HandlerFunc for reviewRPCMethod. Empty
// or malformed params, or an invalid level, is a real KindInvalidInput
// refusal -- never a zero-value dispatch to the provider. The eventual
// call goes through review.Review (plugins/review/plugin.go), the exact
// same package-level reviewProvider seam cmd.go's runReview reads -- so a
// caller reaching this method and a caller reaching `cascade review` get
// identical dispatch behavior, never two divergent implementations.
func reviewRPCHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "plugin.review.review: params required (level, diff)")
	}
	var p reviewRPCParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.review.review: decode params")
	}
	level := provider.ReviewCRLevel(p.Level)
	if !level.Valid() {
		return nil, cascade.Newf(cascade.KindInvalidInput, "plugin.review.review: invalid level %q", p.Level)
	}
	return reviewplugin.Review(ctx, provider.ReviewRequest{Level: level, Diff: p.Diff})
}
