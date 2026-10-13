// Purpose: the daemon composition root's bridge wiring and custody hook:
//
//	wireCascadePABridge (environment reads) over mountCascadePABridge (the
//	bridge subsystem, bridge.send registration and the drain),
//	stopCascadePABridge (the shutdown order), the custody hook that keeps
//	every custody selection this composition root makes off the operator's
//	real keychain, and the bridge's pair-code adapter. Split out of
//	plugin_rpc.go to stay under Art.10.3's 300-line cap. This file carries the
//	cmd-rpc-server-boundary exemption (.golangci.yml + boundary_test.go) on
//	plugin_rpc.go's grounds: it REGISTERS on the daemon's own registry and
//	never dials the daemon.
//
// Inputs: a custody site name and its secrets.Config; the bridge runtime.
// Outputs: the (possibly rewritten) custody config; bridge.send and
//
//	pa.pair_code on the daemon registry.
//
// Constraints: daemonCustodyHook is nil in production and assigned only
//
//	from a test file (TestDaemonLifecycleLint fails on any other
//	assignment). The poll context is the daemon's run context
//	(daemonWiring.Ctx): no signal.NotifyContext and no bare go statement
//	here (the P1-CORE-05 lifecycle lint).
//
// SPORT: cmd/cascade plugin_rpc_bridge (CHANGED, bridge.send route).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
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

// wireCascadePABridge mounts Epic W's Telegram bridge on this daemon: the poll
// goroutine as a tracked subsystem, its drain on shutdown, bridge.send and
// pa.pair_code so `cascade pa pair` issues codes through the process that
// verifies them.
//
// It never fails the daemon's startup. A bridge that cannot be assembled at all
// (an unreadable data directory, a malformed module manifest) is recorded as a
// FAILED subsystem and the daemon continues; a bridge that is merely not
// enabled (the shipped state) is recorded as disabled. Refusing to start the
// daemon because an opt-in chat bridge is misconfigured would trade a missing
// convenience for a dead host.
//
// The custody selection is made HERE, in the composition root, and handed in:
// a bridge that selected its own could not be exercised without reaching the
// operator's real keychain (R-14.206), the same reason wireChatHandlers takes
// its custody as a parameter.
//
// The poll runs under ctx, the daemon's run context. composeDaemon cancels it
// before it joins the supervised goroutines, which ends the poll and lets the
// tracked drain return before the store closes. httpClient is the bot API
// client withBridgeTransport supplied, or nil (the telegram default).
func wireCascadePABridge(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest,
	paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus, httpClient *http.Client) {
	if manifest == nil {
		return
	}
	mountCascadePABridge(ctx, registry, manifest, plugins.BridgeDeps{
		DataDir:    paths.DataDir(),
		Vault:      daemonCustodyConfig(custodySiteBridge, bridgeCustodyConfig(paths.DataDir())),
		Clock:      clock,
		Events:     bus,
		HTTPClient: httpClient,
	}, clock)
}

// mountCascadePABridge is wireCascadePABridge past its environment reads: the
// custody selection and the data directory arrive in deps, so a composition
// test can mount a REAL enabled bridge over a file vault and a recording Bot
// API transport (deps.Transport) without the operator's keychain or the
// network.
func mountCascadePABridge(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest,
	deps plugins.BridgeDeps, clock runtime.Clock) {
	// bridge.send registers UNCONDITIONALLY: a harness process dialing the
	// socket must never see "method not found" because of this daemon's own
	// bridge state; BridgeSend answers "skipped" while no bridge is armed and
	// ErrBridgeSendUnavailable once the bridge is stopping.
	plugins.RegisterBridgeSend(registry)
	rt, err := plugins.NewCascadePABridge(ctx, deps)
	if err != nil {
		manifest.RegisterBridgeModule(ctx, registry, daemon.BridgeSubsystem{DisabledReason: err.Error()})
		return
	}
	if rt.Start == nil {
		// Not enabled: no poll to stop. pa.pair_code is still registered, and
		// answers with the runtime's own typed refusal naming what is missing.
		manifest.RegisterBridgeModule(ctx, registry, daemon.BridgeSubsystem{
			IssueCode: bridgePairCodeIssuer(rt), DisabledReason: rt.DisabledReason,
		})
		return
	}
	plugins.WireApprovalBridge(rt) // FLAG-0: arm the running approval queue
	manifest.RegisterBridgeModule(ctx, registry, daemon.BridgeSubsystem{
		// bridge.send is armed only once the poll has started, so a Start that
		// fails never leaves a sender armed with no drain registered to unwire it.
		Start: func(c context.Context) error {
			if err := rt.Start(c); err != nil {
				return err
			}
			plugins.WireChatBridge(rt, clock)
			return nil
		},
		Stop:      func(c context.Context) error { return stopCascadePABridge(c, rt) },
		IssueCode: bridgePairCodeIssuer(rt),
	})
}

// stopCascadePABridge is the enabled bridge's drain, in this order:
// (1) un-wire bridge.send, refusing new calls and waiting for admitted ones;
// (2) drain the chat bridge's in-flight Send and inbound handling; only then
// (3) stop the module and close its durable state. The reverse order let a
// bridge.send reach a stopped module, or a closed store that misreports the
// thread as "not bound" with a false bridge.refused event. Every step runs
// even when an earlier one's budget ran out, so the state is always closed.
// The first failure is returned (a raw errors.Join would mint a kindless error
// at this boundary); later ones are logged, never dropped.
func stopCascadePABridge(c context.Context, rt *plugins.BridgeRuntime) error {
	unwireErr := plugins.UnwireChatBridge(c)
	drainErr := rt.ChatBridge.Drain(c)
	var first error
	for _, err := range []error{unwireErr, drainErr, rt.Stop(c)} {
		switch {
		case err == nil:
		case first == nil:
			first = err
		default:
			slog.Default().Warn("cascade-pa bridge: stop step failed", "error", err)
		}
	}
	return first
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
