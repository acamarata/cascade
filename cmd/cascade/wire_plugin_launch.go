//go:build !windows

// Purpose: registers the daemon's plugin launch environment: after the
// egress firewall is up, every trusted process plugin the daemon launches
// records into the daemon's store and effect log and publishes on its bus.
// Inputs: the daemon wiring's Store, Bus, Clock, Paths and run context.
// Outputs: plugins.SetLaunchEnvSource replaced with a source over w.Store,
// w.Bus and the daemon's effect log; plugin effect recovery run once.
// Constraints: the effect log is openEffectLog's (no second audit.Log). When
// the run context ends, every launched plugin child is closed (its process
// group reaped) and the previous source is restored.
// SPORT: cmd/cascade daemon registrations (plugin launch, P1-PLG-03).
package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/plugins"
)

// pluginLaunchWired is the launch environment the last daemon wiring
// installed (status and tests read which store and bus it holds).
var pluginLaunchWired atomic.Pointer[plugins.LaunchEnv]

// pluginLaunchShutdownGrace bounds closing launched plugins at shutdown.
const pluginLaunchShutdownGrace = 15 * time.Second

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-launch", Phase: phaseCore, Order: 16, // after egress-firewall (15)
	Wire: wirePluginLaunch,
})

// wirePluginLaunch installs the daemon's launch environment source.
func wirePluginLaunch(w *daemonWiring) error {
	ctx := w.Ctx
	env, err := launchEnvOver(ctx, w.Store, w.Bus, w.Clock, w.Paths)
	if err != nil {
		return err
	}
	pluginLaunchWired.Store(&env)
	restore := plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) { return env, nil })
	context.AfterFunc(ctx, func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginLaunchShutdownGrace)
		defer cancel()
		_ = plugins.CloseLaunched(closeCtx)
		restore()
	})
	return nil
}
