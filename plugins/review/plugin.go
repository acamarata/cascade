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
//	registry (C/S-05.T7, R-16.58), disabled by default (no CommandSpec is
//	declared yet -- P1-E25-W5-S52-T5 adds one), and expose the injectable
//	ReviewProvider seam internal/plugins/review_wiring.go wires to the
//	real internal/review engine at process boot.
//
// Inputs: none at import time; RunCommand's arguments once T5 declares a
//
//	command for this plugin to dispatch.
//
// Outputs: a plugin.BuiltinRegistration reachable from plugin.Builtins();
//
//	the reviewProvider seam, ready for review_wiring.go's init() to
//	overwrite via SetReviewProvider.
//
// Constraints: imports pkg/plugin and pkg/provider only, never internal/**
//
//	(Art.10.2, plugins-providers-boundary depguard rule).
//
// SPORT: plugins/review entity (ADD) -- P1-E25-W5-S52-T4.
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
// byte-for-byte consistent with manifest.toml. Provides is intentionally
// empty -- see manifest.toml's own header comment: P1-E25-W5-S52-T5 adds
// the "review" CommandSpec.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade Review",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
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

// RunCommand: cascade-review declares no commands yet (manifest.Provides
// is empty -- P1-E25-W5-S52-T5 adds the "review" CommandSpec and this
// method's real dispatch to reviewProvider.Review). Every name is
// therefore a genuine "unknown command", not a stub standing in for a
// missing implementation.
func (handlers) RunCommand(_ context.Context, name string, _ []string) error {
	return fmt.Errorf("cascade-review: unknown command %q: this plugin declares no commands yet (P1-E25-W5-S52-T5)", name)
}
