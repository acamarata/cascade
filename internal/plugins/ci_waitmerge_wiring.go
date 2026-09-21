package plugins

// Purpose (this file): the PLUGIN-HOST CALL PATH bridge merge-on-green
// dispatches cascade-github.prs.merge through (R-21.270): internal/ci may
// not import internal/plugins/process (Art.10.2 -- internal/ci is core),
// so this file, the one package allowed to import both plugins/** and
// internal/**, is where ci.MergeCaller meets a real O/S-31.T3
// process.Handle, exactly as ci_policy_wiring.go bridges internal/ci's
// never-pay RouteResolver to plugins/github/cipolicy.
//
// Inputs: none at import time. Each call resolves the running
// cascade-github process's Handle through the injected HandleProvider.
//
// Outputs: a ci.MergeCaller a composition root wires into
// ci.MergeDeps.Caller (see waitmerge_merge.go's own doc comment for why
// the interface, not a concrete type, crosses that boundary).
//
// Constraints (HONEST GAP, recorded rather than papered over): no code
// path in this tree can produce a live process.Handle for cascade-github
// today. dispatch.go's ProvisionElevated documents why: every process-tier
// install evaluates through process.ProcessRuntime.Launch's real trust
// gate, which always refuses process.TrustTierUntrusted before spawning
// anything, and no ticket has added a mechanism to mark any manifest
// trusted yet. noRunningPluginHandle is therefore the correct DEFAULT
// HandleProvider today: a typed, actionable refusal naming the real reason,
// never a fabricated Handle or a silent nil-returning success. A future
// ticket that adds trust elevation and a live-Handle registry replaces
// this default without changing ci.MergeCaller's shape at all.
//
// SPORT: internal/plugins ci-waitmerge-wiring/ADDED (P1-E25-W5-S51-T3).

import (
	"context"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Compile-time proof that *process.Handle satisfies ci.MergeCaller with NO
// adapter code: the two method sets are identical by construction (see
// waitmerge_merge.go's header comment). If process.Handle.Call's signature
// ever drifts from ci.MergeCaller's, this line -- not a runtime surprise --
// is what breaks the build.
var _ ci.MergeCaller = (*process.Handle)(nil)

// cascadeGitHubPluginID is the manifest id this bridge resolves a live
// Handle for.
const cascadeGitHubPluginID = "cascade-github"

// HandleProvider resolves the live process.Handle for a running
// process-tier plugin, or a refusal when none is running.
type HandleProvider func(ctx context.Context, pluginID string) (*process.Handle, error)

// NewGitHubMergeCaller returns a ci.MergeCaller that resolves
// cascade-github's live Handle through provider on every call and
// dispatches through its real stdio transport. Passing a nil provider
// makes every call refuse rather than panic.
func NewGitHubMergeCaller(provider HandleProvider) ci.MergeCaller {
	return &githubMergeCaller{provider: provider}
}

type githubMergeCaller struct {
	provider HandleProvider
}

// Call implements ci.MergeCaller.
func (c *githubMergeCaller) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	if c.provider == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins: the github merge caller has no HandleProvider")
	}
	h, err := c.provider(ctx, cascadeGitHubPluginID)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, cascade.New(cascade.KindInternal, "plugins: the HandleProvider returned no Handle and no error")
	}
	return h.Call(ctx, method, params)
}

// NewGitHubMergeCallerForHost is the PRODUCTION entry point
// cmd/cascade/github_ci_cmd.go builds merge-on-green's transport with
// (P1-E25-W5-S51-T3, D1). It returns either a working ci.MergeCaller or the
// unmet prerequisite that makes an unattended merge impossible today, as a
// typed error naming that prerequisite -- never a caller that would refuse
// later with a less specific message, and never a fabricated success.
//
// Today it always returns the refusal: the trust-elevation mechanism a
// process-tier Launch needs does not exist (see this file's header comment
// and internal/plugins/dispatch.go's ProvisionElevated). The day it does,
// this function returns the real caller and nothing else on this path
// changes.
func NewGitHubMergeCallerForHost(ctx context.Context) (ci.MergeCaller, error) {
	if _, err := noRunningPluginHandle(ctx, cascadeGitHubPluginID); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"plugins: `cascade github ci merge-on-green` needs a live %s process to dispatch prs.merge through, "+
				"and this build has no trust-elevation path that can launch one. The elevated `plugin add` "+
				"install flow that can mark a process-tier manifest trusted is P1-E24-W5-S50-T4's; until it "+
				"lands, no process-tier plugin starts", cascadeGitHubPluginID)
	}
	return NewGitHubMergeCaller(noRunningPluginHandle), nil
}

// noRunningPluginHandle is the default HandleProvider (see this file's
// header comment for why it refuses rather than launches anything).
func noRunningPluginHandle(_ context.Context, pluginID string) (*process.Handle, error) {
	return nil, cascade.Newf(cascade.KindUnavailable,
		"plugins: no running %s process is registered with this host yet -- process-tier plugins have no "+
			"trust-elevation/launch path in this build (see internal/plugins/dispatch.go's ProvisionElevated)",
		pluginID)
}
