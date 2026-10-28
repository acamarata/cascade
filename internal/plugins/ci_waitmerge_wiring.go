package plugins

// Purpose (this file): the PLUGIN-HOST CALL PATH bridge merge-on-green
// dispatches cascade-github.prs.merge through (R-21.270): internal/ci may
// not import internal/plugins/process (Art.10.2 -- internal/ci is core), so
// this file is where ci.MergeCaller meets a live, recorded TrustedHandle.
//
// Inputs: none at import time. Each call launches cascade-github through the
// injected HandleProvider (production: InstalledHandleProvider, i.e.
// LaunchInstalled), calls it once, and closes it.
//
// Outputs: a ci.MergeCaller a composition root wires into
// ci.MergeDeps.Caller, or the typed refusal naming
// `cascade plugin add cascade-github` when no trusted record exists.
//
// Constraints: the launch, the call and their effect records are
// trusted_launch.go's; this file never builds a process.Handle itself.
//
// SPORT: internal/plugins ci-waitmerge-wiring (P1-E25-W5-S51-T3; live handle P1-PLG-03).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/pkg/cascade"
)

// A TrustedHandle satisfies ci.MergeCaller with no adapter code.
var _ ci.MergeCaller = (*TrustedHandle)(nil)

// cascadeGitHubPluginID is the manifest id this bridge launches.
const cascadeGitHubPluginID = "cascade-github"

// closeGrace bounds the Close a per-call caller runs after its call.
const closeGrace = 10 * time.Second

// NewGitHubMergeCaller returns a ci.MergeCaller that launches cascade-github
// through provider on every call, dispatches through its live transport and
// closes it. A nil provider makes every call refuse rather than panic.
func NewGitHubMergeCaller(provider HandleProvider) ci.MergeCaller {
	return &githubMergeCaller{provider: provider}
}

type githubMergeCaller struct {
	provider HandleProvider
}

// Call implements ci.MergeCaller.
func (c *githubMergeCaller) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	return callOnce(ctx, c.provider, "merge", method, params)
}

// callOnce launches cascade-github through provider, makes one call, and
// closes the handle whatever the call returned.
func callOnce(ctx context.Context, provider HandleProvider, what, method string, params []byte) ([]byte, error) {
	if provider == nil {
		return nil, cascade.Newf(cascade.KindInvalidInput, "plugins: the github %s caller has no HandleProvider", what)
	}
	h, err := provider(ctx, cascadeGitHubPluginID)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, cascade.New(cascade.KindInternal, "plugins: the HandleProvider returned no Handle and no error")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
		defer cancel()
		_ = h.Close(closeCtx)
	}()
	return h.Call(ctx, method, params)
}

// mergeHandleProvider is the HandleProvider NewGitHubMergeCallerForHost
// builds its caller over; a test may swap it (restoring it at cleanup).
var mergeHandleProvider = InstalledHandleProvider()

// NewGitHubMergeCallerForHost is the PRODUCTION entry point
// cmd/cascade/github_ci_cmd.go builds merge-on-green's transport with. It
// returns the caller when cascade-github is installed as an enabled, trusted
// process plugin in the launch environment's store, and otherwise the typed
// refusal naming `cascade plugin add cascade-github`. The check launches
// nothing; the caller launches on each call.
func NewGitHubMergeCallerForHost(ctx context.Context) (ci.MergeCaller, error) {
	if err := installedPrereq(ctx, "`cascade github ci merge-on-green` needs a live %s process to dispatch prs.merge through"); err != nil {
		return nil, err
	}
	return NewGitHubMergeCaller(mergeHandleProvider), nil
}

// installedPrereq refuses, naming `cascade plugin add cascade-github`, unless
// the launch environment resolves and holds an enabled, trusted
// cascade-github record. need is the caller's sentence (one %s: the id).
func installedPrereq(ctx context.Context, need string) error {
	env, err := ResolveLaunchEnv(ctx)
	if err == nil {
		_, err = trustedRecord(ctx, env, cascadeGitHubPluginID)
	}
	if err == nil {
		return nil
	}
	return cascade.Wrapf(cascade.KindUnavailable, err, "plugins: "+need+
		"; run `cascade plugin add %s` (an owner-attested add) first", cascadeGitHubPluginID, cascadeGitHubPluginID)
}
