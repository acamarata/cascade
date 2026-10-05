// Purpose: the daemon composition root's custody hook, which the daemon tests
//
//	use to keep every custody selection this composition root makes off the
//	operator's real keychain, and the bridge's pair-code adapter. Split out
//	of plugin_rpc.go to stay under Art.10.3's 300-line cap; it registers
//	nothing on the RPC registry itself.
//
// Inputs: a custody site name and its secrets.Config; the bridge runtime.
// Outputs: the (possibly rewritten) custody config; pa.pair_code's issuer.
// Constraints: daemonCustodyHook is nil in production and assigned only
//
//	from a test file (TestDaemonLifecycleLint fails on any other
//	assignment).
//
// SPORT: cmd/cascade plugin.add handler (CHANGED, custody hook split).
package main

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/secrets"
)

// The custody sites daemonCustodyHook is consulted for.
const (
	custodySiteChat   = "chat"
	custodySiteBridge = "bridge"
)

// daemonCustodyHook, when non-nil, rewrites the custody selection the daemon
// composition root makes for the chat scrub pipeline (registerDBPathHandlers)
// and for the bridge vault (wireCascadePABridge). It is nil in production, so
// both selections are exactly the {Service, Dir} configs those sites build. The cmd/cascade
// test binary sets it in init() (daemon_test_custody_test.go) to force the
// file vault with a failing Runner, so no test reaches the login keychain
// through either site.
var daemonCustodyHook func(site string, cfg secrets.Config) secrets.Config

// daemonCustodyConfig returns cfg, rewritten by daemonCustodyHook when one is
// set.
func daemonCustodyConfig(site string, cfg secrets.Config) secrets.Config {
	if daemonCustodyHook == nil {
		return cfg
	}
	return daemonCustodyHook(site, cfg)
}

// bridgePairCodeIssuer adapts the plugin runtime's issuance closure onto the
// daemon's wire result. The RFC3339 rendering happens here, at the boundary,
// so neither side carries the other's formatting choice.
func bridgePairCodeIssuer(rt *plugins.BridgeRuntime) func(context.Context, string) (daemon.BridgePairCodeResult, error) {
	return func(ctx context.Context, subject string) (daemon.BridgePairCodeResult, error) {
		res, err := rt.IssueCode(ctx, subject)
		if err != nil {
			return daemon.BridgePairCodeResult{}, err
		}
		return daemon.BridgePairCodeResult{
			Code:      res.Code,
			Subject:   res.Subject,
			ExpiresAt: res.ExpiresAt.UTC().Format(time.RFC3339),
		}, nil
	}
}
