// Package review is the cascade-review builtin plugin skin
// (P1-E25-W5-S52-T4): the manifest and the pkg/plugin.RegisterBuiltin
// registration shim for the native adversarial code reviewer implemented
// in internal/review. Art.1.3 symbol split: this package holds ONLY the
// plugin skin (manifest, registration, the injectable ReviewProvider seam)
// -- no engine symbol (ReviewRequest, Finding, a CR template, the
// ReviewProvider implementation) lives here, matching plugins/claude and
// plugins/pbd's own precedent (a plugin package imports pkg/** only, never
// internal/**, Art.10.2; the engine that DOES import internal/conductor
// lives one layer down, in internal/review).
//
// Purpose: register cascade-review with the host's compile-time builtin
//
//	registry (C/S-05.T7, R-16.58) and expose the injectable ReviewProvider
//	seam internal/plugins/review_wiring.go wires to the real internal/review
//	engine at process boot. P1-E25-W5-S52-T5 (cmd.go) adds the "review"
//	CommandSpec declared in manifest() below plus RunCommand's real
//	dispatch to it. FIX (T0 decisions D1/D3, 2026-09-21, superseding the
//	REWORK verdict's BLOCK 1/MAJOR 2): `cascade review` IS now mounted as
//	a top-level noun -- cmd/cascade/review_mount.go's mountReviewCmd, the
//	one root.AddCommand call this package's own files_scope could not
//	reach, called from root.go's mountSubcommands alongside mountChatCmd.
//	That same new file also registers the daemon JSON-RPC method
//	"plugin.review.review" against the SAME reviewProvider seam below,
//	through the exported Review() wrapper this fix adds.
//
// Inputs: none at import time; RunCommand's "review" case takes the raw
//
//	argument vector a caller (the builtin registry's NewCobraCommand, or a
//	direct RunCommand call) passes through.
//
// Outputs: a plugin.BuiltinRegistration reachable from plugin.Builtins(),
//
//	now declaring the "review" CommandSpec; the reviewProvider seam, ready
//	for review_wiring.go's init() to overwrite via SetReviewProvider; and
//	RunCommand("review", args)'s real execution of cmd.go's NewReviewCommand.
//
// Constraints: imports pkg/plugin and pkg/provider only, never internal/**
//
//	(Art.10.2, plugins-providers-boundary depguard rule).
//
// SPORT: plugins/review entity (ADD) -- P1-E25-W5-S52-T4; cmd/CommandSpec
// (ADD) -- P1-E25-W5-S52-T5; Review() export + RPCMethod (ADD) -- FIX
// P1-E25-W5-S52-T5 (D3).
package review

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-review"

// init registers cascade-review with the host's compile-time registry. A
// blank-import of this package (cmd/cascade's composition root performs
// one, via internal/plugins/review_wiring.go's own import) is sufficient
// to make the plugin known to plugin.Builtins().
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns this plugin's cascade.plugin/v2 manifest, kept
// byte-for-byte consistent with manifest.toml. Provides.Commands declares
// exactly one command, "review" (P1-E25-W5-S52-T5, R-16.58): the
// C/S-05.T7 builtin registry's NewCobraCommand("cascade-review","review")
// now returns a real command whose RunE dispatches into RunCommand below.
// RPCMethod is the literal "plugin.review.review" (07-CLI-COMMAND-TREE
// §review's "rpc: plugin.review.* pattern", T0 ruling 2026-09-21 D3) --
// NOT BuiltinRegistry.RPCMethodName's generic "plugin.<pluginID>.
// <commandName>" shape, which would derive "plugin.cascade-review.review"
// instead; cmd/cascade/review_mount.go registers the literal value this
// field names, on the SAME reviewProvider seam, through Review() below.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade Review",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: []plugin.CommandSpec{
				{
					Name:        "review",
					Description: "Run a CR-A/CR-B/CR-C adversarial code review over a diff.",
					RPCMethod:   "plugin.review.review",
				},
			},
		},
	}
}

// reviewProvider is the active provider.ReviewProvider this plugin
// delegates to. internal/plugins/review_wiring.go's init() overwrites it
// via SetReviewProvider with a real internal/review-backed implementation
// at process boot (mirroring plugins/claude/install.go's Generate seam);
// unwiredReviewProvider is the honest default for a build that omits that
// wiring.
var reviewProvider provider.ReviewProvider = unwiredReviewProvider{}

// SetReviewProvider installs p as the active provider. A nil p is refused
// rather than silently leaving the plugin unusable without saying why.
func SetReviewProvider(p provider.ReviewProvider) error {
	if p == nil {
		return fmt.Errorf("cascade-review: SetReviewProvider: provider must not be nil")
	}
	reviewProvider = p
	return nil
}

// Review dispatches ctx/req to the active reviewProvider seam -- the exact
// same package-level variable cmd.go's runReview and RunCommand's cobra
// dispatch read from, never a second construction. Exported so
// cmd/cascade/review_mount.go's daemon JSON-RPC handler for
// "plugin.review.review" (D3) can reach it without duplicating cmd.go's
// flag-parsing/validation logic, which stays CLI-only.
func Review(ctx context.Context, req provider.ReviewRequest) (provider.ReviewResponse, error) {
	return reviewProvider.Review(ctx, req)
}

// unwiredReviewProvider is reviewProvider's default: every method reports,
// honestly, that no host has wired a real provider yet -- never a
// fabricated result.
type unwiredReviewProvider struct{}

func (unwiredReviewProvider) Review(context.Context, provider.ReviewRequest) (provider.ReviewResponse, error) {
	return provider.ReviewResponse{}, fmt.Errorf("cascade-review: review provider not wired (internal/plugins/review_wiring.go must call review.SetReviewProvider)")
}

func (unwiredReviewProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, fmt.Errorf("cascade-review: review provider not wired (internal/plugins/review_wiring.go must call review.SetReviewProvider)")
}

// handlers is the real plugin.BuiltinHandlers implementation for
// cascade-review. It carries no state: RunCommand delegates to the
// package-level reviewProvider seam.
type handlers struct{}

// DispatchTool: cascade-review declares no tools (manifest Provides.Tools
// is empty), so any call is a genuine "unsupported" outcome, not a stub.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-review: no such tool %q: this plugin declares no tools", name)
}

// DispatchIntent: cascade-review declares no intents, for the same reason
// DispatchTool does not.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("cascade-review: no such intent %q: this plugin declares no intents", name)
}

// RunCommand services the "review" CommandSpec (P1-E25-W5-S52-T5): it
// builds a fresh *cobra.Command from NewReviewCommand (cmd.go — which owns
// the --diff/--pr/--level/--json flag surface), sets args as its raw
// argument vector, and executes it, matching
// plugins/cascade-pa/commands.go's handlers.RunCommand precedent exactly
// (pacmd.NewChatCommand(); c.SetArgs(args); c.ExecuteContext(ctx)). Any
// other name is a genuine "unknown command" refusal -- this plugin
// declares exactly one.
func (handlers) RunCommand(ctx context.Context, name string, args []string) error {
	if name != "review" {
		return fmt.Errorf("cascade-review: unknown command %q: this plugin declares one command, \"review\"", name)
	}
	c := NewReviewCommand()
	c.SetArgs(args)
	return c.ExecuteContext(ctx)
}
